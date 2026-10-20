package research

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNominatedControlsRequireReviewAndDoNotChangeScores(t *testing.T) {
	c := fixture(1, "development", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), true)
	p := Packet{Input: c.Input, Digest: c.Digest}
	roster := ControlRoster{Schema: 1, Source: "owner nomination", Candidates: []ControlCandidate{{Login: "candidate", ChannelID: 1, Status: "nominated"}, {Login: "unresolved", Status: "unresolved"}}}
	cs, err := ControlCases([]Packet{p, p}, roster)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || len(cs[0].Reviews) != 0 || !cs[0].Challenge || cs[0].Nomination == nil {
		t.Fatalf("nomination became an ordinary label: %+v", cs)
	}
	if _, known := agreed(cs[0], "chat"); known {
		t.Fatal("owner trust became ground truth")
	}
	observations, err := Observe(cs, "development", "chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 || observations[0].Score == 0 {
		t.Fatal("nominated channel received a scoring override")
	}
	blind, _ := json.Marshal(Blind(p))
	if strings.Contains(string(blind), "nomination") {
		t.Fatal("nomination leaked to blind reviewer")
	}
}
func TestUnresolvedControlsCannotAcquireAnIdentityImplicitly(t *testing.T) {
	roster := ControlRoster{Schema: 1, Source: "owner", Candidates: []ControlCandidate{{Login: "typo", Status: "unresolved", ChannelID: 1}}}
	if _, err := ControlCases(nil, roster); err == nil {
		t.Fatal("unresolved identity admitted to control selection")
	}
}
