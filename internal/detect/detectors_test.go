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
	if q < 79 || q > 81 { // 1 - (0.4 * 0.5)
		t.Errorf("quotient %.1f", q)
	}
	if c <= 0 || c >= 1 {
		t.Errorf("confidence %.2f", c)
	}
}
