package api

import (
	"math"
	"testing"
)

func TestFitEngagementRecoversLine(t *testing.T) {
	// perK = exp(3 - 0.3*ln(v)) - 0.1, with alternating noise so SD > 0.
	var vs, ks []float64
	for i := range 40 {
		v := 100 * math.Pow(1.15, float64(i))
		noise := 0.1
		if i%2 == 1 {
			noise = -0.1
		}
		vs = append(vs, v)
		ks = append(ks, math.Exp(3-0.3*math.Log(v)+noise)-0.1)
	}
	// One extreme outlier that the refit should drop.
	vs = append(vs, 2000)
	ks = append(ks, 0.01)
	f, ok := fitEngagement(vs, ks, minFitGlobal)
	if !ok {
		t.Fatal("fit failed")
	}
	if math.Abs(f.Slope+0.3) > 0.02 || math.Abs(f.A-3) > 0.15 {
		t.Errorf("got a=%.3f slope=%.3f, want about 3 and -0.3", f.A, f.Slope)
	}
	if f.N != 40 {
		t.Errorf("outlier not removed: n=%d", f.N)
	}
	if math.Abs(f.SD-0.1) > 0.02 {
		t.Errorf("sd=%.3f, want about 0.1", f.SD)
	}
}

func TestFitEngagementNeedsPoints(t *testing.T) {
	if _, ok := fitEngagement([]float64{100, 200, 0}, []float64{5, 4, 3}, minFitLang); ok {
		t.Error("expected no fit with too few points")
	}
	// Zero and negative inputs are skipped, not fitted.
	vs := make([]float64, 20)
	ks := make([]float64, 20)
	for i := range vs {
		vs[i] = 0
		ks[i] = 1
	}
	if _, ok := fitEngagement(vs, ks, minFitLang); ok {
		t.Error("expected no fit when every viewer count is zero")
	}
}

func TestScoreOnsetPartners(t *testing.T) {
	m := func(mins ...int64) []int64 {
		out := make([]int64, len(mins))
		for i, x := range mins {
			out[i] = x*60 + 17 // seconds within the minute must not matter
		}
		return out
	}
	own := m(100, 200, 300, 400)
	partners := []onsetPartner{
		{Login: "close", Onsets: m(103, 198, 500), OverlapMinutes: 600},
		{Login: "far", Onsets: m(104, 250, 350), OverlapMinutes: 600},
		{Login: "dense", Onsets: m(400, 300, 200, 100), OverlapMinutes: 10},
	}
	scoreOnsetPartners(own, partners, 3)
	if partners[0].Matches != 2 {
		t.Errorf("close: matches=%d, want 2", partners[0].Matches)
	}
	if partners[1].Matches != 0 {
		t.Errorf("far: matches=%d, want 0 (4 minutes is outside ±3)", partners[1].Matches)
	}
	if partners[2].Matches != 4 {
		t.Errorf("dense (unsorted input): matches=%d, want 4", partners[2].Matches)
	}
	// 3 onsets * 7 minutes / 600 overlap * 4 own onsets = 0.14
	if math.Abs(partners[0].Expected-0.14) > 1e-9 {
		t.Errorf("close: expected=%v, want 0.14", partners[0].Expected)
	}
	// Chance is capped at 1 per own onset.
	if partners[2].Expected != 4 {
		t.Errorf("dense: expected=%v, want 4", partners[2].Expected)
	}
}

func TestPickOnsetPartners(t *testing.T) {
	ps := []onsetPartner{
		{Login: "a", Onsets: make([]int64, 3), OverlapMinutes: 120, Matches: 2, Expected: 1.5},
		{Login: "b", Onsets: make([]int64, 3), OverlapMinutes: 120, Matches: 2, Expected: 0.2},
		{Login: "c", Onsets: make([]int64, 3), OverlapMinutes: 120, Matches: 0},
		{Login: "short", Onsets: make([]int64, 3), OverlapMinutes: 30, Matches: 3},
		{Login: "few", Onsets: make([]int64, 2), OverlapMinutes: 120, Matches: 2},
		{Login: "named", Onsets: make([]int64, 2), OverlapMinutes: 120, Matches: 1},
	}
	got, tested := pickOnsetPartners(ps, "named", 2)
	if tested != 3 {
		t.Errorf("tested=%d, want 3 (a, b, c)", tested)
	}
	var logins []string
	for _, p := range got {
		logins = append(logins, p.Login)
	}
	want := []string{"named", "b"}
	if len(logins) != len(want) || logins[0] != want[0] || logins[1] != want[1] {
		t.Errorf("picked %v, want %v", logins, want)
	}
	if !got[0].Named {
		t.Error("named partner not flagged")
	}
}
