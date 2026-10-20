package research

import (
	"fmt"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/score"
)

type PersistenceReport struct {
	Snapshots             int     `json:"snapshots"`
	AlertEpisodes         int     `json:"alert_episodes"`
	ReviewedFalseEpisodes int     `json:"reviewed_false_episodes"`
	ObservedHours         float64 `json:"observed_hours"`
	LongestAlertMinutes   float64 `json:"longest_alert_minutes"`
	Note                  string  `json:"note"`
}

// Persistence bounds carry-forward to 30 minutes and splits on unknown or
// insufficient observations. It never counts repeated alerts as independent streams.
func Persistence(pool []Packet, cs []Case, target string, threshold, coverage float64) (PersistenceReport, error) {
	if err := ValidateCases(cs); err != nil {
		return PersistenceReport{}, err
	}
	if !slices.Contains(targets, target) || threshold < 0 || threshold > 1 || coverage <= 0 || coverage > 1 {
		return PersistenceReport{}, fmt.Errorf("invalid target, threshold or coverage")
	}
	labels := map[int64]Case{}
	for _, c := range cs {
		if _, ok := labels[c.Input.Ref.ID]; ok {
			return PersistenceReport{}, fmt.Errorf("one whole-stream case required for persistence labels")
		}
		labels[c.Input.Ref.ID] = c
	}
	ps := slices.Clone(pool)
	slices.SortFunc(ps, func(a, b Packet) int {
		if a.Input.Ref.ID < b.Input.Ref.ID {
			return -1
		}
		if a.Input.Ref.ID > b.Input.Ref.ID {
			return 1
		}
		return a.Input.ObservedAt.Compare(b.Input.ObservedAt)
	})
	out := PersistenceReport{Note: "Chronological whole-stream labels only. Alert state carries at most 30 minutes; gaps and abstention break episodes. This is monitoring workload, not independent-trial accuracy."}
	var prev Packet
	active := false
	duration := 0.0
	for _, p := range ps {
		if err := p.Verify(); err != nil {
			return out, err
		}
		if !prev.Input.ObservedAt.IsZero() && prev.Input.Ref.ID == p.Input.Ref.ID && prev.Input.ObservedAt.Equal(p.Input.ObservedAt) {
			continue
		}
		c, ok := labels[p.Input.Ref.ID]
		if !ok {
			continue
		}
		r := score.Evaluate(p.Input, nil)
		m := r.Evidence.Mechanisms[target]
		alert := r.Coverage >= coverage && r.Evidence.Status != "insufficient_evidence" && m.Available && m.Evidence >= threshold
		gap := p.Input.ObservedAt.Sub(prev.Input.ObservedAt)
		contiguous := prev.Input.Ref.ID == p.Input.Ref.ID && gap > 0 && gap <= 30*time.Minute
		if !contiguous {
			active = false
			duration = 0
		} else {
			out.ObservedHours += gap.Hours()
		}
		out.Snapshots++
		if alert && !active {
			out.AlertEpisodes++
			positive, known := agreed(c, target)
			if known && !positive {
				out.ReviewedFalseEpisodes++
			}
		}
		if alert && active && contiguous {
			duration += gap.Minutes()
		} else {
			duration = 0
		}
		out.LongestAlertMinutes = max(out.LongestAlertMinutes, duration)
		active = alert
		prev = p
	}
	return out, nil
}
