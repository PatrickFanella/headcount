package research

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/score"
	"git.subcult.tv/subculture-collective/headcount/internal/store"
)

func fixture(id int64, split string, at time.Time, positive bool) Case {
	h := true
	in := score.Snapshot{Schema: 1, Ref: store.StreamRef{ID: id * 10, ChannelID: id, StartedAt: at, FirstSeenAt: at, LastSeenAt: at.Add(179 * time.Minute), Ended: true}, Accounts: store.AccountStats{Total: 500, Resolved: 500, CoverageKnown: true}}
	for i := range 180 {
		ts := at.Add(time.Duration(i) * time.Minute)
		in.Series.Viewers = append(in.Series.Viewers, store.ViewerPoint{TS: ts, Viewers: 1000})
		in.Series.Chat = append(in.Series.Chat, store.ChatPoint{TS: ts, Messages: 50, Chatters: 20, Healthy: &h})
		in.Series.Followers = append(in.Series.Followers, store.FollowerPoint{TS: ts, Total: 10000 + int64(i)})
	}
	if positive {
		in.Accounts.New = 300
	}
	in.ObservedAt = in.Ref.LastSeenAt.Add(5 * time.Minute)
	digest, _ := Digest(in)
	label := "ordinary"
	if positive {
		label = "consistent"
	}
	review := func(name string) Review {
		return Review{Reviewer: name, Labels: map[string]string{"audience": "indeterminate", "chat": label, "follows": "ordinary"}, Sources: []string{"synthetic software fixture"}}
	}
	return Case{ID: digest, Input: in, Digest: digest, Split: split, Stratum: "synthetic", InclusionProbability: 1, Reviews: []Review{review("one"), review("two")}}
}
func TestPacketIntegrityBlindReviewAndNoOverwrite(t *testing.T) {
	c := fixture(1, "development", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), true)
	p := Packet{Input: c.Input, Digest: c.Digest, Output: score.Evaluate(c.Input, nil)}
	path := filepath.Join(t.TempDir(), "packet.json")
	if err := WriteNew(path, p); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPacket(path); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(path, p); err == nil {
		t.Fatal("overwrote immutable packet")
	}
	b, _ := json.Marshal(Blind(p))
	for _, forbidden := range []string{"quotient", "strength", "synchronized_spikes", "confidence"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("blinded packet leaked %s", forbidden)
		}
	}
	p.Input.Accounts.New++
	if p.Verify() == nil {
		t.Fatal("tampering accepted")
	}
}
func TestGroupAndTimeLeakageRejected(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a, b := fixture(1, "development", at, false), fixture(2, "test", at.Add(24*time.Hour), true)
	if err := ValidateCases([]Case{a, b}); err != nil {
		t.Fatal(err)
	}
	b.Input.Ref.ChannelID = a.Input.Ref.ChannelID
	b.Digest, _ = Digest(b.Input)
	if ValidateCases([]Case{a, b}) == nil {
		t.Fatal("same channel crossed splits")
	}
	b = fixture(2, "test", at.Add(24*time.Hour), true)
	a.Campaign = "same"
	b.Campaign = "same"
	if ValidateCases([]Case{a, b}) == nil {
		t.Fatal("campaign crossed splits")
	}
	a.Campaign = ""
	b.Campaign = ""
	b = fixture(2, "test", at, true)
	if ValidateCases([]Case{a, b}) == nil {
		t.Fatal("overlapping time periods accepted")
	}
}
func TestIndeterminateAbstentionAndRepresentativeWeights(t *testing.T) {
	os := []Observation{{Known: true, Positive: true, Score: 0.8, Coverage: 1, Weight: 2, Hours: 1}, {Known: true, Positive: false, Score: 0.8, Coverage: 1, Weight: 1, Hours: 1}, {Known: true, Positive: true, Score: 0.8, Coverage: 0, Weight: 2, Hours: 1}, {Known: false, Positive: false, Score: 1, Coverage: 1, Weight: 100, Hours: 1}}
	m := Measure(os, 0.5, 0.1)
	if m.Reviewed != 3 || m.Abstained != 1 || m.Precision == nil || *m.Precision != 2.0/3 || m.Recall == nil || *m.Recall != 0.5 {
		t.Fatalf("metrics dropped abstentions/weights: %+v", m)
	}
}
func TestCalibrationFutureHoldoutAndAblation(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cs := []Case{fixture(1, "calibration", at, false), fixture(2, "calibration", at, true), fixture(3, "test", at.Add(24*time.Hour), false), fixture(4, "test", at.Add(24*time.Hour), true)}
	model, err := FitCalibration(cs, "chat", 0.1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := EvaluateCalibration(model, cs)
	if err != nil {
		t.Fatal(err)
	}
	if r.Evaluated != 2 || r.Brier == nil || *r.Brier != 0 {
		t.Fatalf("calibration evaluation: %+v", r)
	}
	metrics, err := Evaluate(cs, "test", "chat", 0.3, 0.1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Representative.Precision == nil || *metrics.Representative.Precision != 1 {
		t.Fatal("fixture not discriminated")
	}
	omitted, err := Evaluate(cs, "test", "chat", 0.3, 0.1, []string{"chatter_accounts"})
	if err != nil {
		t.Fatal(err)
	}
	if omitted.Representative.Alerts != 0 {
		t.Fatal("ablation did not remove detector")
	}
	model.Channels = append(model.Channels, 3)
	if _, err := EvaluateCalibration(model, cs); err == nil {
		t.Fatal("calibration contamination accepted")
	}
}
func TestDisagreementRequiresAdjudication(t *testing.T) {
	c := fixture(1, "test", time.Now(), true)
	c.Reviews[1].Labels["chat"] = "ordinary"
	if _, known := agreed(c, "chat"); known {
		t.Fatal("disagreement silently labeled")
	}
	r := Review{Reviewer: "three", Labels: map[string]string{"audience": "indeterminate", "chat": "consistent", "follows": "ordinary"}, Sources: []string{"adjudicated source"}}
	c.Adjudication = &r
	if positive, known := agreed(c, "chat"); !known || !positive {
		t.Fatal("adjudication ignored")
	}
}
func TestSamplingDeduplicatesStreamsAndIgnoresScores(t *testing.T) {
	at := time.Now()
	var pool []Packet
	for i := int64(1); i <= 4; i++ {
		c := fixture(i, "development", at, i%2 == 0)
		pool = append(pool, Packet{Input: c.Input, Digest: c.Digest})
	}
	pool = append(pool, pool[0])
	a, err := Sample(pool, 2, 17)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Sample(pool, 2, 17)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 2 || a[0].ID != b[0].ID || a[0].InclusionProbability != 0.5 {
		t.Fatal("sample not reproducible or denominator included duplicate snapshots")
	}
}
