package score

import (
	"math"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/store"
)

func TestBuildAlignsSeriesAndChatModes(t *testing.T) {
	t0 := time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC)
	r := store.StreamRef{StartedAt: t0.Add(-time.Minute), FirstSeenAt: t0, LastSeenAt: t0.Add(99 * time.Minute)}
	ser := store.StreamSeries{
		Viewers: []store.ViewerPoint{{TS: t0, Viewers: 100}, {TS: t0.Add(99 * time.Minute), Viewers: 200}},
		Chat:    []store.ChatPoint{{TS: t0.Add(5 * time.Minute), Chatters: 7}},
		Events: []store.EventPoint{
			{TS: t0.Add(-time.Hour), Kind: "chat_mode", Detail: map[string]any{"subs-only": 1.0}},
			{TS: t0.Add(50 * time.Minute), Kind: "chat_mode", Detail: map[string]any{"subs-only": 0.0}},
			{TS: t0.Add(60 * time.Minute), Kind: "raid_in", Detail: map[string]any{"viewers": 500.0}},
		},
	}
	d := Build(r, ser)
	if len(d.Viewers) != 100 || d.Viewers[0] != 100 || d.Viewers[99] != 200 || !math.IsNaN(d.Viewers[1]) {
		t.Fatalf("viewers misaligned: len %d", len(d.Viewers))
	}
	if d.Chatters[5] != 7 {
		t.Errorf("chatters[5] = %v", d.Chatters[5])
	}
	if !d.ObservedFromStart {
		t.Error("expected ObservedFromStart")
	}
	if !d.RestrictedChat { // subscriber-only for the first 50 of 100 minutes
		t.Error("expected RestrictedChat")
	}
	if len(d.Raids) != 1 || d.Raids[0].Minute != 60 || d.Raids[0].Viewers != 500 {
		t.Errorf("raids = %+v", d.Raids)
	}
}

func TestBaselineZ(t *testing.T) {
	var peers []store.PeerStat
	for i := range 40 {
		v := 1000.0 * float64(i+1)
		// Engagement falls with size, with a little alternating noise.
		perK := 50 * math.Pow(v, -0.3) * (1 + 0.1*float64(i%2*2-1))
		peers = append(peers, store.PeerStat{MedianViewers: v, ChattersPerK: perK})
	}
	b := fitBaseline(peers)
	if !b.ok {
		t.Fatal("baseline not fitted")
	}
	if z := b.z(20000, 50*math.Pow(20000, -0.3)); math.Abs(z) > 1 {
		t.Errorf("typical stream z = %.2f", z)
	}
	if z := b.z(20000, 50*math.Pow(20000, -0.3)/5); z > -3 {
		t.Errorf("quiet stream z = %.2f, want < -3", z)
	}
}

func TestLanguageBaselines(t *testing.T) {
	var peers []store.PeerStat
	for i := range 40 {
		v := 1000.0 * float64(i+1)
		lang, scale := "en", 1.0
		if i%2 == 1 {
			lang, scale = "ja", 0.2 // a quieter chat culture
		}
		perK := 50 * math.Pow(v, -0.3) * scale * (1 + 0.05*float64(i%3-1))
		peers = append(peers, store.PeerStat{Language: lang, MedianViewers: v, ChattersPerK: perK})
	}
	b := fitBaselines(peers)
	ja, perLang := b.For("ja")
	if !perLang {
		t.Fatal("no ja baseline")
	}
	quietJA := 50 * math.Pow(20000, -0.3) * 0.2
	if z := ja.z(20000, quietJA); math.Abs(z) > 1.5 {
		t.Errorf("typical ja stream z = %.2f against ja baseline", z)
	}
	if _, perLang := b.For("de"); perLang {
		t.Error("de has no peers but got its own baseline")
	}
}

func TestCategoryOffsets(t *testing.T) {
	var peers []store.PeerStat
	for i := range 80 {
		v := 1000.0 * float64(i%40+1)
		game, name, scale := "509658", "Just Chatting", 1.6
		if i%2 == 1 {
			game, name, scale = "21779", "League of Legends", 0.6
		}
		perK := 50 * math.Pow(v, -0.3) * scale * (1 + 0.05*float64(i%3-1))
		peers = append(peers, store.PeerStat{GameID: game, GameName: name, MedianViewers: v, ChattersPerK: perK})
	}
	b := fitBaselines(peers)
	typicalLoL := 50 * math.Pow(20000, -0.3) * 0.6
	global := b.Expect("", "", "")
	lol := b.Expect("", "21779", "")
	if lol.Category != "League of Legends" || lol.Family != "games" || lol.CategoryPeers != 40 {
		t.Fatalf("expectation %+v", lol)
	}
	if zg, zc := global.Z(20000, typicalLoL), lol.Z(20000, typicalLoL); math.Abs(zc) >= math.Abs(zg) {
		t.Errorf("category baseline z %.2f not closer to 0 than global %.2f", zc, zg)
	}
	jc := b.Expect("", "509658", "")
	if jc.Family != "social" || jc.FamilyOffset <= 0 {
		t.Errorf("Just Chatting expectation %+v", jc)
	}
	// A category made entirely of very quiet streams cannot shift by more than the cap.
	for i := range 30 {
		v := 1000.0 * float64(i+1)
		peers = append(peers, store.PeerStat{GameID: "1", GameName: "Botted Game", MedianViewers: v,
			ChattersPerK: 50 * math.Pow(v, -0.3) * 0.01})
	}
	b = fitBaselines(peers)
	if e := b.Expect("", "1", ""); e.CategoryOffset+e.FamilyOffset < -maxOffsetSD*e.line.sd-1e-9 {
		t.Errorf("offset %.2f exceeds cap", e.CategoryOffset+e.FamilyOffset)
	}
}
