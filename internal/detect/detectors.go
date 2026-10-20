package detect

import (
	"maps"
	"math"
	"slices"
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
	RestrictedChat    bool
	RestrictedMinutes []bool // participation restricted at each minute
	ContextMinutes    []bool // observed passive distribution or collaboration
	ChatHealthy       []bool // full minute of transport and join coverage
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
	Coverage  *float64           `json:"coverage,omitempty"`
	Reason    string             `json:"reason,omitempty"` // why it is unavailable
}

func unavailable(reason string) Component {
	return Component{Reason: reason}
}

// Prepared holds derived series shared by several detectors.
type Prepared struct {
	Viewers  []float64 // observed counts, retaining gaps and repeated values
	Baseline []float64 // rolling median
	Rel      []float64 // (viewers - baseline) / baseline
	Coverage float64   // fraction of minutes with any sample
	// Noise is the median relative change between consecutive Helix updates.
	// Some channels' counts jump 10% or more on every refresh, in either
	// direction; spike detectors cannot tell spikes from that noise.
	Noise float64
}

// MaxNoise is the update-to-update noise above which spike-based detectors
// stand down.
const MaxNoise = 0.08

// Noisy reports whether the viewer count is too erratic for spike detection.
func (p Prepared) Noisy() bool { return p.Noise > MaxNoise }

func (p Prepared) noisy() Component {
	return Component{Reason: "Twitch viewer count too erratic between updates",
		Values: map[string]float64{"update_noise": p.Noise}}
}

const baselineWindow = 31

func Prepare(d StreamData) Prepared {
	p := Prepared{Coverage: Coverage(d.Viewers), Noise: updateNoise(d.Viewers)}
	v := slices.Clone(d.Viewers)
	if len(v) == 0 {
		return p
	}
	p.Viewers = v
	p.Baseline = RollingMedian(v, baselineWindow)
	p.Rel = make([]float64, len(v))
	for i := range v {
		p.Rel[i] = nan
		if !math.IsNaN(v[i]) && p.Baseline[i] > 0 && !contextAt(d, i-baselineWindow/2, i+baselineWindow/2+1) {
			p.Rel[i] = (v[i] - p.Baseline[i]) / p.Baseline[i]
		}
	}
	return p
}

// PeriodicSpikes looks for viewer excursions that repeat at a fixed period.
// Periodicity can also arise from scheduled organic events or counting
// behavior; this detector describes shape and does not identify a supplier.
func PeriodicSpikes(p Prepared) Component {
	n := len(p.Rel)
	if n < 90 || p.Coverage < 0.7 || Coverage(p.Rel) < 0.7 {
		return unavailable("too short")
	}
	if p.Noisy() {
		return p.noisy()
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
		"cycles": 0, "update_noise": p.Noise}
	if bestLag == 0 {
		return Component{Available: true, Values: values}
	}
	values["cycles"] = float64(n) / float64(bestLag)
	strength := ramp(bestACF, 0.2, 0.6) * ramp(skew, -0.2, 1.0) * ramp(amp, 0.03, 0.12)
	return Component{Available: true, Strength: strength, Values: values}
}

// UnexplainedJumps counts sharp, lasting viewer level shifts that no raid or
// game change accounts for.
func UnexplainedJumps(d StreamData, p Prepared) Component {
	n := len(p.Viewers)
	if n < 60 || p.Coverage < 0.7 {
		return unavailable("too short")
	}
	if p.Noisy() {
		return p.noisy()
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
		if math.IsNaN(p.Viewers[i]) || math.IsNaN(p.Viewers[i-3]) || contextAt(d, i-5, i+10) {
			continue
		}
		delta := p.Viewers[i] - p.Viewers[i-3]
		threshold := math.Max(0.08*p.Baseline[i], 300)
		if math.Abs(delta) < threshold {
			continue
		}
		// Count only level shifts that persist. A change that reverts within
		// minutes is either a spike (scored elsewhere) or Helix flip-flopping
		// between cached values.
		before := Median(p.Viewers[i-13 : i-3])
		after := Median(p.Viewers[i : i+10])
		if math.IsNaN(before) || math.IsNaN(after) || math.Abs(after-before) < threshold || math.Signbit(after-before) != math.Signbit(delta) {
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

// SpikeChatLift asks whether chat grows when viewers spike. Each spike is
// compared with the minutes just before it, so slow drifts over a stream
// (chat thinning as it goes on) do not count. Real arrivals bring chat
// roughly in proportion (ratio near 1); bot viewers bring none (near 0).
//
// Helix counts lag real viewers by a few minutes while chat is immediate, so
// the spike window starts a few minutes before the detected onset. Spikes
// near a raid or category change, or in the first minutes, are skipped. The
// strength needs several material spikes: one or two, or spikes barely above
// the noise, are not evidence.
func SpikeChatLift(d StreamData, p Prepared) Component {
	if len(p.Rel) < 90 || p.Coverage < 0.7 {
		return unavailable("too short")
	}
	if p.Noisy() {
		return p.noisy()
	}
	ep := spikeEpisodes(d, p)
	if len(ep) == 0 {
		return unavailable("no eligible spike episodes")
	}
	ratio, n, lift, ok := episodeLift(d.Chatters, ep, 1)
	if !ok {
		return unavailable("no chat around spikes")
	}
	strength := ramp(ratio, 0.5, 0.05) * ramp(n, 2, 5) * ramp(lift, 0.06, 0.15)
	return Component{Available: true, Strength: strength, Values: map[string]float64{
		"lift_ratio": ratio, "spikes": n, "median_spike_rel": lift}}
}

// episodeLift is the median, over spikes, of the activity's relative change
// divided by the viewers' relative change. minBase skips spikes whose
// activity before was too low to measure a change.
func episodeLift(activity []float64, ep []episode, minBase float64) (ratio, n, lift float64, ok bool) {
	var ratios, lifts []float64
	for _, e := range ep {
		b, okB := meanCovered(activity, e.before)
		x, okS := meanCovered(activity, e.during)
		if !okB || !okS || b < minBase {
			continue
		}
		ratios = append(ratios, (x/b-1)/e.lift)
		lifts = append(lifts, e.lift)
	}
	if len(ratios) == 0 {
		return 0, 0, 0, false
	}
	return Median(ratios), float64(len(ratios)), Median(lifts), true
}

type episode struct {
	before, during [2]int // half-open minute ranges
	lift           float64
}

// spikeEpisodes finds viewer spikes that are not explained by a raid or a
// category change, with a window before and during each.
func spikeEpisodes(d StreamData, p Prepared) []episode {
	const threshold = 0.08
	explained := func(i int) bool {
		for _, r := range d.Raids {
			if r.Minute >= i-6 && r.Minute <= i+2 {
				return true
			}
		}
		for _, c := range d.Changes {
			if c >= i-5 && c <= i+3 {
				return true
			}
		}
		return false
	}
	var out []episode
	for _, i := range SpikeOnsets(p, threshold) {
		if i < 20 || explained(i) || contextAt(d, i-15, i+15) || restrictedRange(d, i-15, i+15) {
			continue
		}
		end := i
		for end < len(p.Rel) && end < i+15 && p.Rel[end] >= threshold/2 {
			end++
		}
		if end-i < 2 {
			continue
		}
		before := [2]int{i - 15, i - 5}
		vb := Mean(p.Viewers[before[0]:before[1]])
		vs := Mean(p.Viewers[i:end])
		if vb <= 0 {
			continue
		}
		lift := vs/vb - 1
		if lift < 0.05 {
			continue
		}
		out = append(out, episode{before: before, during: [2]int{i - 4, end}, lift: lift})
	}
	return out
}

// meanCovered averages a series over a range when at least 60% of it is present.
func meanCovered(v []float64, r [2]int) (float64, bool) {
	var sum float64
	n := 0
	for i := max(0, r[0]); i < r[1] && i < len(v); i++ {
		if !math.IsNaN(v[i]) {
			sum += v[i]
			n++
		}
	}
	if n == 0 || float64(n) < 0.6*float64(r[1]-r[0]) {
		return 0, false
	}
	return sum / float64(n), true
}

// SpikeFollowLift asks whether follows accelerate when viewers spike, using
// the same per-spike comparison as SpikeChatLift.
func SpikeFollowLift(d StreamData, p Prepared) Component {
	if len(p.Rel) < 90 || p.Coverage < 0.7 {
		return unavailable("too short")
	}
	if p.Noisy() {
		return p.noisy()
	}
	ep := spikeEpisodes(d, p)
	if len(ep) == 0 {
		return unavailable("no eligible spike episodes")
	}
	ratio, n, lift, ok := episodeLift(FollowDeltas(d.Followers), ep, 0.2)
	if !ok {
		return unavailable("no follower data around spikes")
	}
	strength := ramp(ratio, 0.4, 0.0) * ramp(n, 2, 5) * ramp(lift, 0.06, 0.15)
	return Component{Available: true, Strength: strength, Values: map[string]float64{
		"lift_ratio": ratio, "spikes": n, "median_spike_rel": lift}}
}

// FollowDeltas estimates rates only across adjacent or two-minute intervals.
// Longer intervals stay unknown for episode timing; anomaly counting uses
// FollowerIntervals directly instead of these inferred minute rates.
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
		if prev >= 0 && i-prev <= 2 {
			per := (x - totals[prev]) / float64(i-prev)
			for j := prev + 1; j <= i; j++ {
				out[j] = per
			}
		}
		prev = i
	}
	return out
}

// FollowAnomalies flags unusual observed net-change intervals. It does not
// identify individual follow events or the cause of a follower drop.
func FollowAnomalies(d StreamData) (bursts, purges Component) {
	intervals := FollowerIntervals(d.Followers)
	intervals = slices.DeleteFunc(intervals, func(x FollowerInterval) bool { return contextAt(d, x.End-x.Minutes, x.End+1) })
	if len(d.Followers) < 60 || len(intervals) < 6 {
		return unavailable("few observed follower intervals"), unavailable("few observed follower intervals")
	}
	rates := make([]float64, len(intervals))
	covered := 0
	for i, x := range intervals {
		rates[i] = x.Delta / float64(x.Minutes)
		covered += x.Minutes
	}
	if covered*2 < len(d.Followers) {
		return unavailable("sparse follower intervals"), unavailable("sparse follower intervals")
	}
	med := Median(rates)
	mad := math.Max(MAD(rates), 1)
	var nBurst, nPurge int
	var burstFollows, purged float64
	for _, x := range intervals {
		rate := x.Delta / float64(x.Minutes)
		if rate > med+10*mad && rate > math.Max(50, 4*med) {
			nBurst++
			burstFollows += x.Delta - med*float64(x.Minutes)
		}
		if rate < -math.Max(100, 10*mad) {
			nPurge++
			purged -= x.Delta
		}
	}
	hours := float64(covered) / 60
	gained := 0.0
	for _, x := range intervals {
		gained += x.Delta
	}
	values := map[string]float64{"bursts": float64(nBurst), "burst_follows": burstFollows, "median_per_min": med, "gained": gained, "observed_intervals": float64(len(intervals)), "covered_minutes": float64(covered)}
	bursts = Component{Available: true, Strength: ramp(float64(nBurst)/hours, 0.2, 1.5), Values: values, Reason: "net changes between observations; timing within each interval unknown"}
	purges = Component{Available: true, Strength: ramp(purged, 200, 2000), Values: map[string]float64{"drops": float64(nPurge), "purged": purged}, Reason: "net follower loss; removal cause unknown"}
	return
}

// ChatRepetition measures how much of chat is the same message repeated.
func ChatRepetition(d StreamData) Component {
	var top, msgs float64
	minutes := 0
	for i := range d.Messages {
		if math.IsNaN(d.Messages[i]) || d.Messages[i] < 20 || restrictedAt(d, i) {
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
	if !d.ObservedFromStart || len(p.Viewers) < 60 || contextAt(d, 0, min(15, len(p.Viewers))) {
		return unavailable("start not observed")
	}
	if p.Noisy() {
		return p.noisy()
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
	if len(d.Viewers) < 30 {
		return ChatStats{}, false
	}
	var viewers, ratios []float64
	for i, v := range d.Viewers {
		if i >= len(d.Chatters) || math.IsNaN(v) || math.IsNaN(d.Chatters[i]) || v < 100 || restrictedAt(d, i) || contextAt(d, i, i+1) {
			continue
		}
		viewers = append(viewers, v)
		ratios = append(ratios, d.Chatters[i]/v*1000)
	}
	cov := float64(len(ratios)) / float64(len(d.Viewers))
	if len(ratios) < 30 || cov < 0.5 {
		return ChatStats{ChatCoverage: cov}, false
	}
	return ChatStats{MedianViewers: Median(viewers), ChattersPerK: Median(ratios), ChatCoverage: cov}, true
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

// AccountAgeComponent scores young accounts. Unavailable accounts are diagnostic only.
func AccountAgeComponent(chatters, newAccounts, missing int) Component {
	if chatters < 50 {
		return unavailable("few chatters")
	}
	newShare := float64(newAccounts) / float64(chatters)
	missShare := float64(missing) / float64(chatters)
	s := ramp(newShare, 0.15, 0.4)
	return Component{Available: true, Strength: s, Values: map[string]float64{
		"chatters": float64(chatters), "new_account_share": newShare, "unavailable_share": missShare}}
}

// Weights for combining components. They are hand-set and uncalibrated.
var Weights = map[string]float64{
	"periodic_spikes":     0.6,
	"synchronized_spikes": 0.5,
	"spike_chat_lift":     0.5,
	"chat_engagement":     0.5,
	"follower_purge":      0.4,
	"chatter_accounts":    0.4,
	"shared_accounts":     0.4,
	"follow_bursts":       0.3,
	"unexplained_jumps":   0.3,
	"spike_follow_lift":   0.25,
	"chat_repetition":     0.15,
	"start_ramp":          0.1,
}

// Families group detectors that read the same underlying evidence. A
// passive audience-source change can affect shape, response and engagement
// together, so those all share a family. These groups are conservative
// dependency assumptions, not a claim of statistical independence.
var Families = map[string]string{
	"periodic_spikes": "audience", "synchronized_spikes": "audience",
	"unexplained_jumps": "audience", "start_ramp": "audience",
	"spike_chat_lift": "audience", "spike_follow_lift": "audience",
	"chat_engagement": "audience", "chat_repetition": "chat",
	"chatter_accounts": "chat", "shared_accounts": "chat",
	"follow_bursts": "follows", "follower_purge": "follows",
}

// SingleCap is the most one shared-cause family contributes without support
// from another. One detector can misfire on an unusual but innocent stream;
// higher scores require another conservative cause group; groups may still correlate.
const SingleCap = 0.3

// Combine merges components with a weighted noisy-OR, giving a quotient from
// 0 to 100. Each component contributes evidence w*strength; evidence within a
// family contributes only its strongest weighted component. Every family is capped at SingleCap plus
// the strongest other family, so one line of evidence alone gives at most 30.
// Confidence reflects how much of the total weight could be evaluated and how
// long the stream was observed.
func Combine(components map[string]Component, minutes int) (quotient, confidence float64) {
	var availW, totalW float64
	evidence := map[string]float64{}
	for _, name := range slices.Sorted(maps.Keys(Weights)) {
		w := Weights[name]
		totalW += w
		c := components[name]
		if !c.Available {
			continue
		}
		coverage := 1.0
		if c.Coverage != nil {
			coverage = math.Max(0, math.Min(1, *c.Coverage))
		}
		availW += w * coverage
		strength := c.Strength
		if math.IsNaN(strength) || math.IsInf(strength, 0) {
			continue
		}
		f := Families[name]
		evidence[f] = math.Max(evidence[f], w*math.Max(0, math.Min(1, strength)))
	}
	keep := 1.0
	for _, f := range slices.Sorted(maps.Keys(evidence)) {
		other := 0.0
		for g, e := range evidence {
			if g != f {
				other = math.Max(other, e)
			}
		}
		keep *= 1 - math.Min(evidence[f], SingleCap+other)
	}
	quotient = 100 * (1 - keep)
	confidence = (availW / totalW) * math.Min(1, math.Max(0, float64(minutes)/180))
	return
}

// SharedAccounts describes young authors observed across tracked channels.
// Community overlap, promotions and utility bots can produce this too.
// minOther is how many other channels an account must appear in.
func SharedAccounts(chatters, young int, otherChannels []int, minOther int) Component {
	if chatters < 50 {
		return unavailable("few chatters")
	}
	shared := 0
	for _, n := range otherChannels {
		if n >= minOther {
			shared++
		}
	}
	share := float64(shared) / float64(chatters)
	s := ramp(share, 0.005, 0.04) * ramp(float64(shared), 2, 8)
	return Component{Available: true, Strength: s, Values: map[string]float64{
		"chatters": float64(chatters), "young_accounts": float64(young),
		"shared_accounts": float64(shared), "shared_share": share}}
}

// updateNoise is the median relative change between consecutive distinct
// viewer values (Helix updates), ignoring held repeats.
func updateNoise(v []float64) float64 {
	var changes []float64
	prev := math.NaN()
	for _, x := range v {
		if math.IsNaN(x) || x == prev {
			continue
		}
		if !math.IsNaN(prev) && prev > 0 {
			changes = append(changes, math.Abs(x-prev)/prev)
		}
		prev = x
	}
	if len(changes) < 10 {
		return 0
	}
	return Median(changes)
}
