package score

import (
	"math"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/detect"
	"git.subcult.tv/subculture-collective/headcount/internal/store"
)

// Snapshot contains the exact aggregate inputs read by one scoring pass.
// It intentionally contains no raw text, credentials, or individual viewer data.
type Snapshot struct {
	BehaviorPeers []store.BehaviorPeer `json:"behavior_peers,omitempty"`
	Schema        int                  `json:"schema"`
	ObservedAt    time.Time            `json:"observed_at"`
	Ref           store.StreamRef      `json:"stream"`
	Series        store.StreamSeries   `json:"series"`
	Peers         []store.PeerStat     `json:"peers"`
	Accounts      store.AccountStats   `json:"accounts"`
	Behavior      map[string]float64   `json:"behavior"`
	Shared        detect.Component     `json:"shared"`
	Partners      []detect.Partner     `json:"partners"`
	GameID        string               `json:"game_id"`
	GameName      string               `json:"game_name"`
}
type Evidence struct {
	Status       string                      `json:"status"`
	Quality      detect.Quality              `json:"quality"`
	Mechanisms   map[string]detect.Mechanism `json:"mechanisms"`
	Experimental map[string]float64          `json:"experimental"`
	Validation   string                      `json:"validation"`
}
type Result struct {
	Peer       *store.PeerStat             `json:"peer_metric,omitempty"`
	Version    int                         `json:"version"`
	Quotient   float64                     `json:"quotient"`
	Coverage   float64                     `json:"coverage"`
	Components map[string]detect.Component `json:"components"`
	Evidence   Evidence                    `json:"evidence"`
}

// Evaluate has no database, network or clock dependency. omit supports family
// and component ablations without changing any frozen observations.
func Evaluate(in Snapshot, omit []string) Result {
	d := Build(in.Ref, in.Series)
	p := detect.Prepare(d)
	cs := map[string]detect.Component{
		"periodic_spikes": detect.PeriodicSpikes(p), "unexplained_jumps": detect.UnexplainedJumps(d, p),
		"spike_chat_lift": detect.SpikeChatLift(d, p), "spike_follow_lift": detect.SpikeFollowLift(d, p),
		"chat_repetition": detect.ChatRepetition(d), "start_ramp": detect.StartRamp(d, p),
	}
	cs["follow_bursts"], cs["follower_purge"] = detect.FollowAnomalies(d)
	own := eligibleOnsets(d, p)
	absolute := make([]int64, len(own))
	start := in.Ref.FirstSeenAt.Truncate(time.Minute).Unix() / 60
	for i, m := range own {
		absolute[i] = start + int64(m)
	}
	if p.Noisy() || p.Coverage < 0.7 {
		cs["synchronized_spikes"] = detect.Component{Reason: "insufficient stable observed viewer data"}
	} else {
		cs["synchronized_spikes"] = detect.Synchrony(absolute, in.Partners, 3)
	}
	stats, metricOK := detect.ChatEngagement(d, p)
	var peer *store.PeerStat
	if metricOK {
		peer = &store.PeerStat{StreamID: in.Ref.ID, ChannelID: in.Ref.ChannelID, StartedAt: in.Ref.StartedAt, EndedAt: in.Ref.LastSeenAt, Language: in.Ref.Language, GameID: in.GameID, GameName: in.GameName, MedianViewers: stats.MedianViewers, ChattersPerK: stats.ChattersPerK}
	}
	b := fitBaselines(selectPeers(in.Peers, in.Ref))
	cs["chat_engagement"] = detect.Component{Reason: "no matched chat data or independent past peers"}
	if metricOK && b.ok() {
		e := b.Expect(in.Ref.Language, in.GameID, in.GameName)
		c := detect.ChatResidualComponent(e.Z(stats.MedianViewers, stats.ChattersPerK), false, map[string]float64{
			"median_viewers": stats.MedianViewers, "chatters_per_k": stats.ChattersPerK, "expected_per_k": e.Expected(stats.MedianViewers), "chat_coverage": stats.ChatCoverage, "category_peers": float64(e.CategoryPeers), "category_offset": round(e.CategoryOffset), "family_offset": round(e.FamilyOffset),
		})
		c.Reason = engagementNote(e)
		cs["chat_engagement"] = c
	}
	a := in.Accounts
	cs["chatter_accounts"] = detect.AccountAgeComponent(a.Resolved, a.New, a.Unavailable)
	cs["shared_accounts"] = in.Shared
	if !a.CoverageKnown || a.Coverage() < 0.8 {
		cs["chatter_accounts"] = detect.Component{Reason: "account creation-date coverage below 80% or legacy denominator unknown"}
		cs["shared_accounts"] = detect.Component{Reason: "account creation-date coverage below 80% or legacy denominator unknown"}
	}
	quality := detect.DataQuality(d)
	quality.AccountCoverage = a.Coverage()
	quality.PendingAccounts = a.Pending
	quality.UnavailableAccounts = a.Unavailable
	for _, p := range in.Series.Chat {
		if p.Healthy == nil {
			quality.LegacyChatMinutes++
		} else if !*p.Healthy {
			quality.PartialChatMinutes++
		}
		quality.DuplicateMessages += p.DuplicateMessages
		quality.DelayedMessages += p.DelayedMessages
		quality.SourceDelayMS += p.SourceDelayMS
	}
	if quality.ContextMinutes > 0 {
		cs["chatter_accounts"] = detect.Component{Reason: "observed distribution/collaboration; per-stream author cohort cannot be localized to unaffected minutes"}
		cs["shared_accounts"] = detect.Component{Reason: "observed collaboration or distribution context; shared-audience explanation unresolved"}
	}
	span := max(1, len(d.Viewers))
	for name, c := range cs {
		coverage := float64(quality.ObservedViewerMinutes) / float64(span)
		switch name {
		case "chat_repetition":
			coverage = float64(quality.HealthyChatMinutes) / float64(span)
		case "chatter_accounts", "shared_accounts":
			coverage = a.Coverage() * float64(quality.HealthyChatMinutes) / float64(span)
		case "chat_engagement", "spike_chat_lift":
			coverage = math.Min(coverage, float64(quality.HealthyChatMinutes)/float64(span))
		case "follow_bursts", "follower_purge", "spike_follow_lift":
			coverage = detect.Coverage(d.Followers)
		}
		c.Coverage = &coverage
		if slices.Contains(omit, name) || slices.Contains(omit, detect.Families[name]) {
			c = detect.Component{Reason: "omitted for ablation"}
		}
		cs[name] = c
	}
	q, cov := detect.Combine(cs, quality.ObservedViewerMinutes)
	status := "no_scored_anomaly"
	if q > 0 {
		status = "observed_anomalies"
	}
	if quality.ObservedViewerMinutes < 30 || cov < 0.1 {
		status = "insufficient_evidence"
	}
	diagnostics := map[string]float64{}
	for k, v := range in.Behavior {
		diagnostics[k] = v
	}
	// Compare historical smoothing as a sensitivity diagnostic only. It is never
	// an independent contribution to the quotient.
	if quality.LongestViewerGap == 0 {
		smooth := detect.Interpolate(detect.HeldToNaN(d.Viewers))
		alt := d
		alt.Viewers = smooth
		diagnostics["interpolated_periodic_strength"] = detect.PeriodicSpikes(detect.Prepare(alt)).Strength
	}
	behaviorComparisons(in, diagnostics)
	diagnostics["circular_shift_match_p"] = detect.CircularMatchP(absolute, in.Partners, start, start+int64(len(d.Viewers)), 3)
	return Result{Peer: peer, Version: Version, Quotient: q, Coverage: cov, Components: cs, Evidence: Evidence{Status: status, Quality: quality, Mechanisms: detect.Mechanisms(cs), Experimental: diagnostics, Validation: "uncalibrated; coverage is not detection probability"}}
}

// One past completed stream per other channel. Sorting makes replay stable.
func selectPeers(peers []store.PeerStat, r store.StreamRef) []store.PeerStat {
	p := slices.Clone(peers)
	slices.SortStableFunc(p, func(a, b store.PeerStat) int { return b.EndedAt.Compare(a.EndedAt) })
	seen := map[int64]bool{}
	out := []store.PeerStat{}
	for _, x := range p {
		if x.ChannelID == r.ChannelID || x.EndedAt.IsZero() || !x.EndedAt.Before(r.StartedAt) || x.EndedAt.Before(r.StartedAt.Add(-7*24*time.Hour)) || seen[x.ChannelID] {
			continue
		}
		seen[x.ChannelID] = true
		out = append(out, x)
	}
	return out
}

func eligibleOnsets(d detect.StreamData, p detect.Prepared) []int {
	if p.Noisy() || p.Coverage < 0.7 {
		return nil
	}
	var out []int
	for _, i := range detect.SpikeOnsets(p, spikeThreshold) {
		explained := false
		for j := max(0, i-6); j < min(len(d.Viewers), i+4); j++ {
			if j < len(d.ContextMinutes) && d.ContextMinutes[j] {
				explained = true
			}
		}
		for _, r := range d.Raids {
			if r.Minute >= i-6 && r.Minute <= i+3 {
				explained = true
			}
		}
		for _, c := range d.Changes {
			if c >= i-6 && c <= i+3 {
				explained = true
			}
		}
		if !explained {
			out = append(out, i)
		}
	}
	return out
}

func behaviorComparisons(in Snapshot, out map[string]float64) {
	peers := slices.Clone(in.BehaviorPeers)
	slices.SortStableFunc(peers, func(a, b store.BehaviorPeer) int { return b.EndedAt.Compare(a.EndedAt) })
	own := []store.BehaviorPeer{}
	other := []store.BehaviorPeer{}
	seen := map[int64]bool{}
	for _, p := range peers {
		if !p.EndedAt.Before(in.Ref.StartedAt) || p.EndedAt.Before(in.Ref.StartedAt.Add(-7*24*time.Hour)) || p.StreamID == in.Ref.ID {
			continue
		}
		if p.ChannelID == in.Ref.ChannelID {
			if len(own) < 10 {
				own = append(own, p)
			}
		} else if !seen[p.ChannelID] {
			other = append(other, p)
			seen[p.ChannelID] = true
		}
	}
	out["behavior_peer_channels"] = float64(len(other))
	out["behavior_own_streams"] = float64(len(own))
	for prefix, rows := range map[string][]store.BehaviorPeer{"peer": other, "own": own} {
		for _, k := range []string{"largest_creation_day_share", "p95_messages", "mean_active_minutes", "command_share", "utility_message_share"} {
			var values []float64
			for _, p := range rows {
				if v, ok := p.Metrics[k]; ok {
					values = append(values, v)
				}
			}
			if len(values) == 0 {
				continue
			}
			out[prefix+"_median_"+k] = detect.Median(values)
			if target, ok := in.Behavior[k]; ok {
				rank := 0.0
				for _, v := range values {
					if v < target {
						rank++
					} else if v == target {
						rank += 0.5
					}
				}
				out[prefix+"_rank_"+k] = rank / float64(len(values))
			}
		}
	}
}
