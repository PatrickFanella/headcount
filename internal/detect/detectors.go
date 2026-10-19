package detect

import (
	"math"
)

// StreamData is one stream aligned to minute indices from its first sample.
type StreamData struct {
	Viewers   []float64 // raw Helix viewer_count; NaN for missing minutes
	Chatters  []float64 // unique chatters per minute; NaN if chat wasn't collected
	Messages  []float64
	TopText   []float64 // occurrences of the most repeated message per minute
	Followers []float64 // follower total; NaN where not sampled
	Raids     []Raid
	Changes   []int // minute indices of game changes
	// ObservedFromStart is true when the first sample came within a few minutes
	// of the stream's Helix started_at.
	ObservedFromStart bool
	// RestrictedChat is true when subscriber-only or emote-only mode was on
	// for a substantial part of the stream.
	RestrictedChat bool
}

type Raid struct {
	Minute  int
	Viewers int
}

// Component is one detector result. Strength is 0 (no evidence) to 1 (strong
// evidence). Available is false when there wasn't enough data to judge.
type Component struct {
	Available bool               `json:"available"`
	Strength  float64            `json:"strength"`
	Values    map[string]float64 `json:"values,omitempty"`
	Reason    string             `json:"reason,omitempty"` // why it is unavailable
}

func unavailable(reason string) Component {
	return Component{Reason: reason}
}

// Prepared holds derived series shared by several detectors.
type Prepared struct {
	Viewers  []float64 // interpolated between real Helix changes
	Baseline []float64 // rolling median
	Rel      []float64 // (viewers - baseline) / baseline
	Coverage float64   // fraction of minutes with any sample
}

const baselineWindow = 31

func Prepare(d StreamData) Prepared {
	p := Prepared{Coverage: Coverage(d.Viewers)}
	v := Interpolate(HeldToNaN(d.Viewers))
	if v == nil {
		return p
	}
	p.Viewers = v
	p.Baseline = RollingMedian(v, baselineWindow)
	p.Rel = make([]float64, len(v))
	for i := range v {
		if p.Baseline[i] > 0 {
			p.Rel[i] = (v[i] - p.Baseline[i]) / p.Baseline[i]
		}
	}
	return p
}

// PeriodicSpikes looks for viewer excursions that repeat at a fixed period.
// Organic streams have event-driven bumps (raids, highlights) and periodic
// dips (ad breaks); repeated upward spikes that return to the same baseline
// are what a rotating view-bot produces.
func PeriodicSpikes(p Prepared) Component {
	n := len(p.Rel)
	if n < 90 || p.Coverage < 0.7 {
		return unavailable("too short")
	}
	bestLag, bestACF := 0, 0.0
	maxLag := min(120, n/3)
	acf := make([]float64, maxLag+2)
	for lag := 4; lag <= maxLag+1; lag++ {
		acf[lag] = Autocorrelation(p.Rel, lag)
	}
	isPeak := func(lag int) bool { return acf[lag] > acf[lag-1] && acf[lag] >= acf[lag+1] }
	for lag := 5; lag <= maxLag; lag++ {
		if isPeak(lag) && acf[lag] > bestACF {
			bestLag, bestACF = lag, acf[lag]
		}
	}
	// Multiples of the true period also peak; prefer the fundamental.
	for lag := 5; lag < bestLag; lag++ {
		if isPeak(lag) && acf[lag] >= 0.85*bestACF {
			bestLag, bestACF = lag, acf[lag]
			break
		}
	}
	skew := Skewness(p.Rel)
	amp := Quantile(p.Rel, 0.95)
	values := map[string]float64{"period_min": float64(bestLag), "acf": bestACF, "skew": skew, "p95_rel": amp,
		"cycles": 0}
	if bestLag == 0 {
		return Component{Available: true, Values: values}
	}
	values["cycles"] = float64(n) / float64(bestLag)
	strength := ramp(bestACF, 0.2, 0.6) * ramp(skew, -0.2, 1.0) * ramp(amp, 0.03, 0.12)
	return Component{Available: true, Strength: strength, Values: values}
}

// UnexplainedJumps counts sharp viewer changes that no raid or game change
// accounts for.
func UnexplainedJumps(d StreamData, p Prepared) Component {
	n := len(p.Viewers)
	if n < 60 || p.Coverage < 0.7 {
		return unavailable("too short")
	}
	explained := func(i int, delta float64) bool {
		for _, r := range d.Raids {
			if r.Minute >= i-5 && r.Minute <= i+1 && float64(r.Viewers) >= 0.3*math.Abs(delta) {
				return true
			}
		}
		for _, c := range d.Changes {
			if c >= i-4 && c <= i+3 {
				return true
			}
		}
		return false
	}
	var jumps, unexplained int
	for i := 15; i < n-10; i++ {
		delta := p.Viewers[i] - p.Viewers[i-3]
		threshold := math.Max(0.08*p.Baseline[i], 300)
		if math.Abs(delta) < threshold {
			continue
		}
		jumps++
		if !explained(i, delta) {
			unexplained++
		}
		i += 5 // count one jump once
	}
	hours := float64(n) / 60
	rate := float64(unexplained) / hours
	return Component{Available: true, Strength: ramp(rate, 0.5, 3), Values: map[string]float64{
		"jumps": float64(jumps), "unexplained": float64(unexplained), "per_hour": rate}}
}

// spikeMinutes returns indices where viewers are well above baseline, and
// the remaining "baseline" indices.
func spikeMinutes(p Prepared) (spike, base []int, meanRel float64) {
	threshold := math.Max(0.05, Quantile(p.Rel, 0.9))
	sum := 0.0
	for i, r := range p.Rel {
		switch {
		case r >= threshold:
			spike = append(spike, i)
			sum += r
		case math.Abs(r) < 0.02:
			base = append(base, i)
		}
	}
	if len(spike) > 0 {
		meanRel = sum / float64(len(spike))
	}
	return
}

// liftRatio compares an activity series during viewer spikes with baseline
// minutes, normalized by the size of the spike. Real viewers bring
// proportional activity (ratio near 1); bot viewers bring none (near 0).
func liftRatio(activity []float64, spike, base []int, meanRel float64) (ratio float64, ok bool) {
	pick := func(idx []int) []float64 {
		out := make([]float64, 0, len(idx))
		for _, i := range idx {
			if i < len(activity) && !math.IsNaN(activity[i]) {
				out = append(out, activity[i])
			}
		}
		return out
	}
	s, b := pick(spike), pick(base)
	if len(s) < 8 || len(b) < 20 {
		return 0, false
	}
	bm := Mean(b)
	if bm <= 0 {
		return 0, false
	}
	return (Mean(s)/bm - 1) / meanRel, true
}

// SpikeChatLift asks whether chat grows when viewers spike.
func SpikeChatLift(d StreamData, p Prepared) Component {
	if len(p.Rel) < 90 {
		return unavailable("too short")
	}
	spike, base, meanRel := spikeMinutes(p)
	if meanRel < 0.08 {
		return Component{Available: true, Values: map[string]float64{"mean_spike_rel": meanRel}}
	}
	ratio, ok := liftRatio(d.Chatters, spike, base, meanRel)
	if !ok {
		return unavailable("no chat")
	}
	return Component{Available: true, Strength: ramp(ratio, 0.5, 0.05), Values: map[string]float64{
		"lift_ratio": ratio, "mean_spike_rel": meanRel, "spike_minutes": float64(len(spike))}}
}

// SpikeFollowLift asks whether follows accelerate when viewers spike.
func SpikeFollowLift(d StreamData, p Prepared) Component {
	if len(p.Rel) < 90 {
		return unavailable("too short")
	}
	spike, base, meanRel := spikeMinutes(p)
	if meanRel < 0.08 {
		return Component{Available: true, Values: map[string]float64{"mean_spike_rel": meanRel}}
	}
	ratio, ok := liftRatio(FollowDeltas(d.Followers), spike, base, meanRel)
	if !ok {
		return unavailable("no follower data")
	}
	return Component{Available: true, Strength: ramp(ratio, 0.4, 0.0), Values: map[string]float64{
		"lift_ratio": ratio, "mean_spike_rel": meanRel}}
}

// FollowDeltas converts follower totals into per-minute gains, spreading a
// gain evenly across unsampled minutes.
func FollowDeltas(totals []float64) []float64 {
	out := make([]float64, len(totals))
	for i := range out {
		out[i] = nan
	}
	prev := -1
	for i, x := range totals {
		if math.IsNaN(x) {
			continue
		}
		if prev >= 0 {
			per := (x - totals[prev]) / float64(i-prev)
			for j := prev + 1; j <= i; j++ {
				out[j] = per
			}
		}
		prev = i
	}
	return out
}

// FollowAnomalies looks for follow bursts and follower purges. Twitch
// periodically removes fake follows, which shows up as a large drop.
func FollowAnomalies(d StreamData) (bursts, purges Component) {
	deltas := FollowDeltas(d.Followers)
	if Coverage(deltas) < 0.5 || len(deltas) < 60 {
		return unavailable("no follower data"), unavailable("no follower data")
	}
	med := Median(deltas)
	mad := math.Max(MAD(deltas), 1)
	var nBurst, nPurge int
	var burstFollows, purged float64
	for _, x := range deltas {
		if math.IsNaN(x) {
			continue
		}
		if x > med+10*mad && x > math.Max(50, 4*med) {
			nBurst++
			burstFollows += x - med
		}
		if x < -math.Max(100, 10*mad) {
			nPurge++
			purged += -x
		}
	}
	gained := 0.0
	first, last := nan, nan
	for _, x := range d.Followers {
		if !math.IsNaN(x) {
			if math.IsNaN(first) {
				first = x
			}
			last = x
		}
	}
	gained = last - first
	hours := float64(len(deltas)) / 60
	bursts = Component{Available: true, Strength: ramp(float64(nBurst)/hours, 0.2, 1.5), Values: map[string]float64{
		"bursts": float64(nBurst), "burst_follows": burstFollows, "median_per_min": med, "gained": gained}}
	purges = Component{Available: true, Strength: ramp(purged, 200, 2000), Values: map[string]float64{
		"drops": float64(nPurge), "purged": purged}}
	return
}

// ChatRepetition measures how much of chat is the same message repeated.
func ChatRepetition(d StreamData) Component {
	var top, msgs float64
	minutes := 0
	for i := range d.Messages {
		if math.IsNaN(d.Messages[i]) || d.Messages[i] < 20 {
			continue
		}
		top += d.TopText[i]
		msgs += d.Messages[i]
		minutes++
	}
	if minutes < 30 {
		return unavailable("quiet chat")
	}
	share := top / msgs
	return Component{Available: true, Strength: ramp(share, 0.35, 0.75), Values: map[string]float64{"top_text_share": share}}
}

// StartRamp measures how quickly viewers reach half the stream's median.
// Notifications make large streams ramp fast, so this carries little weight.
func StartRamp(d StreamData, p Prepared) Component {
	if !d.ObservedFromStart || len(p.Viewers) < 60 {
		return unavailable("start not observed")
	}
	med := Median(p.Viewers)
	minutes := -1
	for i, v := range p.Viewers {
		if v >= med/2 {
			minutes = i
			break
		}
	}
	if minutes < 0 {
		return unavailable("never reached median")
	}
	return Component{Available: true, Strength: ramp(float64(minutes), 3, 0), Values: map[string]float64{"minutes_to_half": float64(minutes)}}
}

// ChatStats summarizes chat engagement for the peer-group comparison done by
// the scorer.
type ChatStats struct {
	MedianViewers float64
	ChattersPerK  float64 // median unique chatters per minute per 1000 viewers
	ChatCoverage  float64 // fraction of viewer minutes with chat data
}

func ChatEngagement(d StreamData, p Prepared) (ChatStats, bool) {
	if len(p.Viewers) < 30 {
		return ChatStats{}, false
	}
	ratios := make([]float64, 0, len(p.Viewers))
	withChat := 0
	for i, v := range p.Viewers {
		if i >= len(d.Chatters) || math.IsNaN(d.Chatters[i]) || v < 50 {
			continue
		}
		withChat++
		ratios = append(ratios, d.Chatters[i]/v*1000)
	}
	cov := float64(withChat) / float64(len(p.Viewers))
	if cov < 0.5 {
		return ChatStats{ChatCoverage: cov}, false
	}
	return ChatStats{MedianViewers: Median(p.Viewers), ChattersPerK: Median(ratios), ChatCoverage: cov}, true
}

// ChatResidualComponent turns a peer-group z-score of log chatters-per-1k
// into a component. Strongly negative z (far quieter chat than similar
// channels) is the suspicious direction.
func ChatResidualComponent(z float64, restricted bool, values map[string]float64) Component {
	s := ramp(z, -1, -3)
	if restricted {
		s *= 0.3
	}
	values["z"] = z
	return Component{Available: true, Strength: s, Values: values}
}

// AccountAgeComponent scores the share of chatters with very new or missing
// (suspended/deleted) accounts.
func AccountAgeComponent(chatters, newAccounts, missing int) Component {
	if chatters < 50 {
		return unavailable("few chatters")
	}
	newShare := float64(newAccounts) / float64(chatters)
	missShare := float64(missing) / float64(chatters)
	s := math.Max(ramp(newShare, 0.15, 0.4), ramp(missShare, 0.03, 0.15))
	return Component{Available: true, Strength: s, Values: map[string]float64{
		"chatters": float64(chatters), "new_account_share": newShare, "missing_share": missShare}}
}

// Weights for combining components. They are hand-set and uncalibrated.
var Weights = map[string]float64{
	"periodic_spikes":   0.6,
	"spike_chat_lift":   0.5,
	"chat_engagement":   0.5,
	"follower_purge":    0.4,
	"chatter_accounts":  0.4,
	"follow_bursts":     0.3,
	"unexplained_jumps": 0.3,
	"spike_follow_lift": 0.25,
	"chat_repetition":   0.15,
	"start_ramp":        0.1,
}

// Combine merges components with a weighted noisy-OR, giving a quotient from
// 0 to 100. Confidence reflects how much of the total weight could be
// evaluated and how long the stream was observed.
func Combine(components map[string]Component, minutes int) (quotient, confidence float64) {
	keep := 1.0
	var availW, totalW float64
	for name, w := range Weights {
		totalW += w
		c, ok := components[name]
		if !ok || !c.Available {
			continue
		}
		availW += w
		keep *= 1 - w*c.Strength
	}
	quotient = 100 * (1 - keep)
	confidence = (availW / totalW) * math.Min(1, float64(minutes)/180)
	return
}
