package detect

import (
	"math"
	"math/rand/v2"
	"testing"
)

// organic builds a slowly varying curve with noise and a few ad-break dips,
// reported through a Helix-like cache that refreshes every 4 minutes.
func organic(n int, seed uint64) []float64 {
	r := rand.New(rand.NewPCG(seed, 1))
	out := make([]float64, n)
	level := 20000.0
	held := 0.0
	for i := range out {
		level += r.NormFloat64() * 150
		v := level * (1 + 0.15*math.Sin(float64(i)/120))
		if i%60 >= 0 && i%60 < 3 {
			v *= 0.93 // ad break dip
		}
		if i%4 == 0 {
			held = math.Round(v)
		}
		out[i] = held
	}
	return out
}

// botted adds 25% spikes lasting 6 minutes every 30 minutes.
func botted(n int, seed uint64) []float64 {
	base := organic(n, seed)
	out := make([]float64, n)
	for i := range out {
		out[i] = base[i]
		if i%30 < 6 {
			out[i] = math.Round(base[i] * 1.25)
		}
	}
	return out
}

func chatFor(viewers []float64, perK float64, followSpikes bool) []float64 {
	p := Prepare(StreamData{Viewers: viewers})
	out := make([]float64, len(viewers))
	for i := range out {
		v := p.Baseline[i]
		if followSpikes {
			v = p.Viewers[i]
		}
		out[i] = math.Round(v * perK / 1000)
	}
	return out
}

func TestPeriodicSpikes(t *testing.T) {
	for seed := range uint64(5) {
		clean := PeriodicSpikes(Prepare(StreamData{Viewers: organic(480, seed)}))
		bad := PeriodicSpikes(Prepare(StreamData{Viewers: botted(480, seed)}))
		if clean.Strength > 0.2 {
			t.Errorf("seed %d: organic strength %.2f (%v)", seed, clean.Strength, clean.Values)
		}
		if bad.Strength < 0.6 {
			t.Errorf("seed %d: botted strength %.2f (%v)", seed, bad.Strength, bad.Values)
		}
		if p := bad.Values["period_min"]; p < 28 || p > 32 {
			t.Errorf("seed %d: period %.0f, want ~30", seed, p)
		}
	}
}

func TestSpikeChatLift(t *testing.T) {
	v := botted(480, 7)
	fake := SpikeChatLift(StreamData{Viewers: v, Chatters: chatFor(v, 20, false)}, Prepare(StreamData{Viewers: v}))
	real := SpikeChatLift(StreamData{Viewers: v, Chatters: chatFor(v, 20, true)}, Prepare(StreamData{Viewers: v}))
	if fake.Strength < 0.8 {
		t.Errorf("chat ignoring spikes: strength %.2f (%v)", fake.Strength, fake.Values)
	}
	if real.Strength > 0.2 {
		t.Errorf("chat following spikes: strength %.2f (%v)", real.Strength, real.Values)
	}
}

func TestFollowAnomalies(t *testing.T) {
	n := 240
	totals := make([]float64, n)
	total := 100000.0
	for i := range totals {
		total += 4
		if i == 100 {
			total += 1500 // burst
		}
		if i == 200 {
			total -= 3000 // purge
		}
		totals[i] = total
	}
	b, p := FollowAnomalies(StreamData{Followers: totals})
	if b.Values["bursts"] != 1 {
		t.Errorf("bursts = %v", b.Values)
	}
	if p.Strength != 1 {
		t.Errorf("purge strength %.2f (%v)", p.Strength, p.Values)
	}
	steady := make([]float64, n)
	for i := range steady {
		steady[i] = 100000 + float64(i*4)
	}
	b, p = FollowAnomalies(StreamData{Followers: steady})
	if b.Strength != 0 || p.Strength != 0 {
		t.Errorf("steady follows flagged: %v %v", b, p)
	}
}

func TestUnexplainedJumpsRespectsRaids(t *testing.T) {
	v := organic(240, 3)
	for i := 120; i < 240; i++ {
		v[i] += 8000
	}
	d := StreamData{Viewers: v}
	if c := UnexplainedJumps(d, Prepare(d)); c.Values["unexplained"] != 1 {
		t.Errorf("unexplained = %v", c.Values)
	}
	d.Raids = []Raid{{Minute: 119, Viewers: 7000}}
	if c := UnexplainedJumps(d, Prepare(d)); c.Values["unexplained"] != 0 {
		t.Errorf("raid not credited: %v", c.Values)
	}
}

func TestInterpolateHeld(t *testing.T) {
	got := Interpolate(HeldToNaN([]float64{10, 10, 10, 10, 20, 20, nan, 20}))
	want := []float64{10, 12.5, 15, 17.5, 20, 20, 20, 20}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestCombine(t *testing.T) {
	q, c := Combine(map[string]Component{
		"periodic_spikes": {Available: true, Strength: 1},
		"spike_chat_lift": {Available: true, Strength: 1},
	}, 300)
	if math.Abs(q-30) > 1e-9 { // both detectors share the audience cause
		t.Errorf("quotient %.1f", q)
	}
	if c <= 0 || c >= 1 {
		t.Errorf("confidence %.2f", c)
	}
}

func TestSynchrony(t *testing.T) {
	// Own spikes every 30 minutes over 8 hours.
	var own []int64
	for m := int64(0); m < 480; m += 30 {
		own = append(own, 1000+m)
	}
	shifted := func(offset int64) []int64 {
		var out []int64
		for _, m := range own {
			out = append(out, m+offset)
		}
		return out
	}
	unrelated := Partner{Login: "a", OverlapMinutes: 480, Onsets: []int64{1007, 1101, 1203, 1290, 1377}}
	twin := Partner{Login: "twin", OverlapMinutes: 480, Onsets: shifted(1)}
	c := Synchrony(own, []Partner{unrelated}, 2)
	if c.Strength > 0.1 {
		t.Errorf("unrelated partner flagged: %v", c.Values)
	}
	c = Synchrony(own, []Partner{unrelated, twin}, 2)
	if c.Strength < 0.9 || c.Reason != "best partner: twin" {
		t.Errorf("twin not flagged: %v %q", c.Values, c.Reason)
	}
	// Everyone spiking together looks platform-wide.
	var crowd []Partner
	for i := range 10 {
		crowd = append(crowd, Partner{Login: string(rune('b' + i)), OverlapMinutes: 480, Onsets: shifted(0)})
	}
	c = Synchrony(own, crowd, 2)
	if c.Values["platform_wide"] != 1 || c.Strength > 0.25 {
		t.Errorf("platform-wide not discounted: %v", c.Values)
	}
}

func TestBinomTail(t *testing.T) {
	if p := binomTail(10, 0, 0.3); p != 1 {
		t.Errorf("P(X>=0) = %v", p)
	}
	if p := binomTail(10, 10, 0.5); math.Abs(p-1.0/1024) > 1e-12 {
		t.Errorf("P(X>=10) = %v", p)
	}
}

func TestUnexplainedJumpsIgnoresFlipFlop(t *testing.T) {
	// Helix alternating between two cached values every 3-4 minutes.
	v := make([]float64, 240)
	for i := range v {
		if (i/3)%2 == 0 {
			v[i] = 2900
		} else {
			v[i] = 3400
		}
	}
	d := StreamData{Viewers: v}
	if c := UnexplainedJumps(d, Prepare(d)); c.Values["unexplained"] != 0 {
		t.Errorf("flip-flop counted as jumps: %v", c.Values)
	}
}

func TestCombineNeedsCorroboration(t *testing.T) {
	alone, _ := Combine(map[string]Component{
		"spike_chat_lift": {Available: true, Strength: 1},
		"periodic_spikes": {Available: true, Strength: 0},
	}, 300)
	if math.Abs(alone-100*SingleCap) > 0.01 {
		t.Errorf("single detector quotient %.1f, want %.0f", alone, 100*SingleCap)
	}
	weak, _ := Combine(map[string]Component{
		"spike_chat_lift":  {Available: true, Strength: 1},
		"chatter_accounts": {Available: true, Strength: 0.1},
	}, 300)
	if weak <= alone || weak > 45 {
		t.Errorf("weakly supported quotient %.1f", weak)
	}
}

// Chat that thins over the stream, with a couple of small unrelated bumps,
// must not read as chat ignoring spikes.
func TestSpikeChatLiftNeedsRepeatedMaterialSpikes(t *testing.T) {
	n := 360
	v := make([]float64, n)
	chat := make([]float64, n)
	for i := range v {
		v[i] = 1000 - float64(i)
		chat[i] = 15 - float64(i)/40
		if (i >= 100 && i < 106) || (i >= 220 && i < 226) {
			v[i] *= 1.12
			chat[i] *= 0.7
		}
	}
	d := StreamData{Viewers: v, Chatters: chat}
	if c := SpikeChatLift(d, Prepare(d)); c.Strength > 0.3 {
		t.Errorf("two small spikes gave strength %.2f (%v)", c.Strength, c.Values)
	}
}

func TestSharedAccounts(t *testing.T) {
	if c := SharedAccounts(20, 5, []int{5, 5}, 2); c.Available {
		t.Error("available with few chatters")
	}
	organic := SharedAccounts(2000, 80, []int{1, 1, 2, 1, 0, 3}, 2)
	if organic.Strength > 0.05 {
		t.Errorf("organic strength %.2f (%v)", organic.Strength, organic.Values)
	}
	others := make([]int, 60)
	for i := range others {
		others[i] = 4
	}
	farm := SharedAccounts(1000, 200, others, 2)
	if farm.Strength < 0.9 {
		t.Errorf("shared farm strength %.2f (%v)", farm.Strength, farm.Values)
	}
}

func TestNoisyCountsDisableSpikeDetectors(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 1))
	v := make([]float64, 300)
	held := 4000.0
	for i := range v {
		if i%4 == 0 {
			held = math.Round(4000 * (1 + 0.25*(r.Float64()*2-1)))
		}
		v[i] = held
	}
	d := StreamData{Viewers: v, Chatters: make([]float64, 300)}
	p := Prepare(d)
	if !p.Noisy() {
		t.Fatalf("noise %.3f not flagged", p.Noise)
	}
	for name, c := range map[string]Component{
		"periodic": PeriodicSpikes(p), "jumps": UnexplainedJumps(d, p), "chat lift": SpikeChatLift(d, p),
	} {
		if c.Available {
			t.Errorf("%s available on a noisy count", name)
		}
	}
	if q := Prepare(StreamData{Viewers: organic(300, 2)}); q.Noisy() {
		t.Errorf("organic count flagged noisy (%.3f)", q.Noise)
	}
}

func TestCombineSameFamilyDoesNotCorroborate(t *testing.T) {
	q, _ := Combine(map[string]Component{
		"spike_chat_lift":   {Available: true, Strength: 1},
		"spike_follow_lift": {Available: true, Strength: 1},
	}, 300)
	if math.Abs(q-100*SingleCap) > 0.01 {
		t.Errorf("chat and follow lift alone gave %.1f, want %.0f", q, 100*SingleCap)
	}
}

func TestCombineIsDeterministic(t *testing.T) {
	comps := map[string]Component{
		"periodic_spikes": {Available: true, Strength: 0.49 / 0.6}, "synchronized_spikes": {Available: true, Strength: 1},
		"unexplained_jumps": {Available: true, Strength: 1}, "start_ramp": {Available: true, Strength: 1},
		"chat_engagement": {Available: true, Strength: 1},
	}
	first, _ := Combine(comps, 200)
	for range 500 {
		if q, _ := Combine(comps, 200); q != first {
			t.Fatalf("quotient %v then %v", first, q)
		}
	}
}
