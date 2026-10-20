package detect

import (
	"math"
	"testing"
)

func TestDependentViewerEvidenceIsCapped(t *testing.T) {
	q, _ := Combine(map[string]Component{
		"periodic_spikes":   {Available: true, Strength: 1},
		"spike_chat_lift":   {Available: true, Strength: 1},
		"spike_follow_lift": {Available: true, Strength: 1},
		"chat_engagement":   {Available: true, Strength: 1},
	}, 300)
	if math.Abs(q-30) > 1e-9 {
		t.Fatalf("shared passive-audience cause gave %v, want 30", q)
	}
}

func TestMissingUsersAreNotSuspensionEvidence(t *testing.T) {
	c := AccountAgeComponent(100, 0, 80)
	if c.Strength != 0 {
		t.Fatalf("unavailable users scored as abuse: %+v", c)
	}
}

func TestFollowerGapIsOneObservedInterval(t *testing.T) {
	totals := make([]float64, 120)
	for i := range totals {
		totals[i] = nan
	}
	for i := 0; i < 120; i += 10 {
		totals[i] = 10000 + float64(i)
	}
	totals[70] += 3000
	for i := 80; i < 120; i += 10 {
		totals[i] += 3000
	}
	b, _ := FollowAnomalies(StreamData{Followers: totals})
	if !b.Available || b.Values["bursts"] != 1 {
		t.Fatalf("one net change became multiple events: %+v", b)
	}
}

func TestPrepareDoesNotInventViewerArrivalBeforeObservation(t *testing.T) {
	p := Prepare(StreamData{Viewers: []float64{100, 100, 100, 100, 200}})
	if p.Viewers[2] != 100 {
		t.Fatalf("count change moved backwards: %v", p.Viewers)
	}
}

func TestCircularDiagnosticRecognizesPeriodicNull(t *testing.T) {
	own := []int64{}
	for i := int64(0); i < 480; i += 30 {
		own = append(own, i)
	}
	p := CircularMatchP(own, []Partner{{OverlapMinutes: 480, Onsets: own}}, 0, 480, 3)
	if p < 0.05 {
		t.Fatalf("periodic common schedule treated as unique alignment: %v", p)
	}
}
