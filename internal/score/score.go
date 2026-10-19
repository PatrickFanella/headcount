// Package score turns recorded stream data into suspicion scores. Finished
// streams get a final score; live streams get a provisional one every
// LiveEvery so the dashboard can show current values.
package score

import (
	"context"
	"log/slog"
	"math"
	"time"

	"git.subcult.tv/subculture-collective/sus-quotient/internal/detect"
	"git.subcult.tv/subculture-collective/sus-quotient/internal/store"
)

// Version increments whenever detector logic or weights change, so old
// scores can be told apart and recomputed.
const Version = 1

const (
	liveEvery       = 15 * time.Minute
	liveMinMinutes  = 60
	newAccountAge   = 30 * 24 * time.Hour
	lowQuotient     = 15
	restoreQuotient = 30
)

type Scorer struct {
	store *store.Store
	log   *slog.Logger

	baseline    *baseline
	baselineAt  time.Time
	lastLiveRun time.Time
}

func New(s *store.Store, log *slog.Logger) *Scorer { return &Scorer{store: s, log: log} }

func (s *Scorer) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if err := s.runOnce(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("scoring pass", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Scorer) runOnce(ctx context.Context) error {
	if time.Since(s.baselineAt) > time.Hour || s.baseline == nil {
		peers, err := s.store.ChatPeerStats(ctx, time.Now().Add(-7*24*time.Hour))
		if err != nil {
			return err
		}
		s.baseline = fitBaseline(peers)
		s.baselineAt = time.Now()
		s.log.Info("chat baseline", "streams", len(peers), "ok", s.baseline.ok,
			"slope", round(s.baseline.slope), "sd", round(s.baseline.sd))
	}

	// The chat collector flushes a minute after it ends; wait a few minutes
	// so the final chat rows are in.
	done, err := s.store.StreamsToScore(ctx, time.Now().Add(-3*time.Minute), 200)
	if err != nil {
		return err
	}
	refs := done
	if time.Since(s.lastLiveRun) >= liveEvery {
		live, err := s.store.LiveStreamsToScore(ctx, liveMinMinutes)
		if err != nil {
			return err
		}
		refs = append(refs, live...)
		s.lastLiveRun = time.Now()
	}
	channels := map[int64]bool{}
	for _, r := range refs {
		if err := s.scoreStream(ctx, r); err != nil {
			s.log.Warn("score stream", "stream", r.ID, "err", err)
			continue
		}
		channels[r.ChannelID] = true
	}
	for id := range channels {
		if err := s.store.UpdateChannelQuotient(ctx, id); err != nil {
			return err
		}
	}
	demoted, restored, err := s.store.ApplyTiers(ctx, lowQuotient, restoreQuotient)
	if err != nil {
		return err
	}
	if len(refs) > 0 || demoted > 0 || restored > 0 {
		s.log.Info("scored", "final", len(done), "provisional", len(refs)-len(done), "channels", len(channels),
			"demoted", demoted, "restored", restored)
	}
	return nil
}

// Build aligns a stream's recorded series to minute indices.
func Build(r store.StreamRef, ser store.StreamSeries) detect.StreamData {
	start := r.FirstSeenAt.Truncate(time.Minute)
	n := int(r.LastSeenAt.Sub(start)/time.Minute) + 1
	idx := func(ts time.Time) int { return int(ts.Sub(start) / time.Minute) }
	series := func() []float64 {
		v := make([]float64, n)
		for i := range v {
			v[i] = math.NaN()
		}
		return v
	}
	d := detect.StreamData{
		Viewers: series(), Chatters: series(), Messages: series(), TopText: series(), Followers: series(),
		ObservedFromStart: r.FirstSeenAt.Sub(r.StartedAt) <= 3*time.Minute,
	}
	for _, p := range ser.Viewers {
		if i := idx(p.TS); i >= 0 && i < n {
			d.Viewers[i] = float64(p.Viewers)
		}
	}
	for _, p := range ser.Chat {
		if i := idx(p.TS); i >= 0 && i < n {
			d.Chatters[i] = float64(p.Chatters)
			d.Messages[i] = float64(p.Messages)
			d.TopText[i] = float64(p.TopText)
		}
	}
	for _, p := range ser.Followers {
		if i := idx(p.TS); i >= 0 && i < n {
			d.Followers[i] = float64(p.Total)
		}
	}
	// Chat mode timeline: count minutes in subscriber-only or emote-only mode.
	restricted := false
	restrictedMinutes := 0
	lastMode := 0
	for _, e := range ser.Events {
		i := max(0, idx(e.TS))
		switch e.Kind {
		case "raid_in":
			v, _ := e.Detail["viewers"].(float64)
			d.Raids = append(d.Raids, detect.Raid{Minute: i, Viewers: int(v)})
		case "game_change":
			d.Changes = append(d.Changes, i)
		case "chat_mode":
			if restricted {
				restrictedMinutes += min(i, n) - lastMode
			}
			subs, _ := e.Detail["subs-only"].(float64)
			emotes, _ := e.Detail["emote-only"].(float64)
			restricted = subs > 0 || emotes > 0
			lastMode = min(i, n)
		}
	}
	if restricted {
		restrictedMinutes += n - lastMode
	}
	d.RestrictedChat = float64(restrictedMinutes) > 0.3*float64(n)
	return d
}

func (s *Scorer) scoreStream(ctx context.Context, r store.StreamRef) error {
	ser, err := s.store.LoadSeries(ctx, r)
	if err != nil {
		return err
	}
	d := Build(r, ser)
	p := detect.Prepare(d)
	comps := map[string]detect.Component{
		"periodic_spikes":   detect.PeriodicSpikes(p),
		"unexplained_jumps": detect.UnexplainedJumps(d, p),
		"spike_chat_lift":   detect.SpikeChatLift(d, p),
		"spike_follow_lift": detect.SpikeFollowLift(d, p),
		"chat_repetition":   detect.ChatRepetition(d),
		"start_ramp":        detect.StartRamp(d, p),
	}
	comps["follow_bursts"], comps["follower_purge"] = detect.FollowAnomalies(d)

	if stats, ok := detect.ChatEngagement(d, p); ok && s.baseline.ok {
		z := s.baseline.z(stats.MedianViewers, stats.ChattersPerK)
		comps["chat_engagement"] = detect.ChatResidualComponent(z, d.RestrictedChat, map[string]float64{
			"median_viewers": stats.MedianViewers, "chatters_per_k": stats.ChattersPerK,
			"expected_per_k": s.baseline.expected(stats.MedianViewers), "chat_coverage": stats.ChatCoverage,
		})
	} else {
		comps["chat_engagement"] = detect.Component{Reason: "no chat data or baseline not ready"}
	}

	total, newAcc, missing, err := s.store.ChatterAccounts(ctx, r.ID, r.StartedAt, newAccountAge)
	if err != nil {
		return err
	}
	comps["chatter_accounts"] = detect.AccountAgeComponent(total, newAcc, missing)

	minutes := len(d.Viewers)
	q, conf := detect.Combine(comps, minutes)
	return s.store.SaveScore(ctx, store.Score{
		StreamID: r.ID, ChannelID: r.ChannelID, Version: Version, Minutes: minutes,
		Quotient: q, Confidence: conf, Final: r.Ended, Components: comps,
	})
}

func round(x float64) float64 { return math.Round(x*1000) / 1000 }
