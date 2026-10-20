package detect

import "math"

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

type Quality struct {
	PartialChatMinutes     int     `json:"partial_chat_minutes"`
	DuplicateMessages      int     `json:"duplicate_messages"`
	DelayedMessages        int     `json:"delayed_messages"`
	SourceDelayMS          int64   `json:"source_delay_ms"`
	PendingAccounts        int     `json:"pending_accounts"`
	UnavailableAccounts    int     `json:"unavailable_accounts"`
	ObservedViewerMinutes  int     `json:"observed_viewer_minutes"`
	DistinctViewerReadings int     `json:"distinct_viewer_readings"`
	LongestViewerGap       int     `json:"longest_viewer_gap"`
	HealthyChatMinutes     int     `json:"healthy_chat_minutes"`
	LegacyChatMinutes      int     `json:"legacy_chat_minutes"`
	FollowerIntervals      int     `json:"follower_intervals"`
	AccountCoverage        float64 `json:"account_coverage"`
	ContextMinutes         int     `json:"context_minutes"`
	RestrictedMinutes      int     `json:"restricted_minutes"`
}

func DataQuality(d StreamData) Quality {
	q := Quality{FollowerIntervals: len(FollowerIntervals(d.Followers))}
	gap := 0
	last := math.NaN()
	for i, v := range d.Viewers {
		if finite(v) {
			q.ObservedViewerMinutes++
			gap = 0
			if math.IsNaN(last) || last != v {
				q.DistinctViewerReadings++
			}
			last = v
		} else {
			gap++
			q.LongestViewerGap = max(q.LongestViewerGap, gap)
		}
		if i < len(d.Chatters) && finite(d.Chatters[i]) {
			if i < len(d.ChatHealthy) && d.ChatHealthy[i] {
				q.HealthyChatMinutes++
			} else {
				q.LegacyChatMinutes++
			}
		}
		if i < len(d.ContextMinutes) && d.ContextMinutes[i] {
			q.ContextMinutes++
		}
		if restrictedAt(d, i) {
			q.RestrictedMinutes++
		}
	}
	return q
}

// Mechanism keeps unlike observations separate; evidence is an index, not a probability.
type Mechanism struct {
	Available bool    `json:"available"`
	Evidence  float64 `json:"evidence"`
}

func Mechanisms(cs map[string]Component) map[string]Mechanism {
	out := map[string]Mechanism{"audience": {}, "chat": {}, "follows": {}}
	for name, w := range Weights {
		c := cs[name]
		if !c.Available {
			continue
		}
		f := Families[name]
		m := out[f]
		m.Available = true
		if finite(c.Strength) {
			m.Evidence = math.Max(m.Evidence, w*math.Max(0, math.Min(1, c.Strength)))
		}
		out[f] = m
	}
	return out
}
