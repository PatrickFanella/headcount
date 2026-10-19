// Package score turns recorded stream data into suspicion scores. Finished
// streams get a final score; live streams get a provisional one every
// LiveEvery so the dashboard can show current values.
package score

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/sus-quotient/internal/detect"
	"git.subcult.tv/subculture-collective/sus-quotient/internal/store"
)

// Version increments whenever detector logic or weights change, so old
// scores can be told apart and recomputed.
const Version = 7

const (
	liveEvery       = 15 * time.Minute
	youngAccountAge = 90 * 24 * time.Hour
	sharedWindow    = 7 * 24 * time.Hour
	sharedMinOther  = 2 // other channels an account must chat in
	liveMinMinutes  = 60
	newAccountAge   = 30 * 24 * time.Hour
	lowQuotient     = 15
	restoreQuotient = 30
	spikeThreshold  = 0.08
)

type Scorer struct {
	store *store.Store
	log   *slog.Logger

	baseline    *baselines
	baselineAt  time.Time
	lastLiveRun time.Time

	// ChatterRetention is how long per-chatter rows are kept after a stream
	// ends; older streams keep only a summary. Zero disables pruning.
	ChatterRetention time.Duration
	lastPrune        time.Time
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
	refit := time.Hour
	if !s.baseline.ok() {
		refit = 10 * time.Minute
	}
	if time.Since(s.baselineAt) > refit {
		s.baselineAt = time.Now()
		peers, err := s.store.ChatPeerStats(ctx, time.Now().Add(-7*24*time.Hour))
		if err != nil {
			// Keep the previous baseline and score with it.
			s.log.Warn("chat baseline", "err", err)
		} else {
			s.baseline = fitBaselines(peers)
			s.log.Info("chat baseline", "streams", len(peers), "ok", s.baseline.ok(),
				"slope", round(s.baseline.global.slope), "sd", round(s.baseline.global.sd),
				"languages", len(s.baseline.byLang))
		}
	}
	if s.baseline == nil {
		s.baseline = &baselines{global: &baseline{}}
	}

	// The chat collector flushes a minute after it ends; wait a few minutes
	// so the final chat rows are in.
	done, err := s.store.StreamsToScore(ctx, time.Now().Add(-3*time.Minute), Version, 200)
	if err != nil {
		return err
	}
	refs := done
	if time.Since(s.lastLiveRun) >= liveEvery-time.Minute { // slack for ticker jitter
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
	if s.ChatterRetention > 0 && time.Since(s.lastPrune) > time.Hour {
		s.lastPrune = time.Now()
		s.prune(ctx)
	}
	return nil
}

// prune summarizes and removes per-chatter rows of old streams, then drops
// cached accounts nothing references. Work is batched to keep transactions short.
func (s *Scorer) prune(ctx context.Context) {
	cutoff := time.Now().Add(-s.ChatterRetention)
	var streams int
	var rows, users int64
	for range 100 {
		n, r, err := s.store.PruneChatters(ctx, cutoff, newAccountAge, 50)
		if err != nil {
			s.log.Warn("prune chatters", "err", err)
			return
		}
		streams, rows = streams+n, rows+r
		if n == 0 {
			break
		}
	}
	for range 100 {
		n, err := s.store.PruneUsers(ctx, cutoff, 10000)
		if err != nil {
			s.log.Warn("prune users", "err", err)
			return
		}
		users += n
		if n == 0 {
			break
		}
	}
	size, _ := s.store.DatabaseSize(ctx)
	s.log.Info("retention", "streams", streams, "chatter_rows", rows, "users", users, "db_bytes", size)
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

	sync, err := s.synchrony(ctx, r, p)
	if err != nil {
		return err
	}
	comps["synchronized_spikes"] = sync

	if stats, ok := detect.ChatEngagement(d, p); ok && s.baseline.ok() {
		e := s.baseline.Expect(r.Language, mainGame(ser.Viewers), "")
		z := e.Z(stats.MedianViewers, stats.ChattersPerK)
		langFlag := 0.0
		if e.PerLanguage {
			langFlag = 1
		}
		c := detect.ChatResidualComponent(z, d.RestrictedChat, map[string]float64{
			"median_viewers": stats.MedianViewers, "chatters_per_k": stats.ChattersPerK,
			"expected_per_k": e.Expected(stats.MedianViewers), "chat_coverage": stats.ChatCoverage,
			"language_baseline": langFlag, "category_peers": float64(e.CategoryPeers),
			"category_offset": round(e.CategoryOffset), "family_offset": round(e.FamilyOffset),
		})
		c.Reason = engagementNote(e)
		comps["chat_engagement"] = c
	} else {
		comps["chat_engagement"] = detect.Component{Reason: "no chat data or baseline not ready"}
	}

	total, newAcc, missing, err := s.store.ChatterAccounts(ctx, r.ID, r.StartedAt, newAccountAge)
	if err != nil {
		return err
	}
	comps["chatter_accounts"] = detect.AccountAgeComponent(total, newAcc, missing)
	shared, err := s.sharedAccounts(ctx, r, total)
	if err != nil {
		return err
	}
	comps["shared_accounts"] = shared

	minutes := len(d.Viewers)
	q, conf := detect.Combine(comps, minutes)
	return s.store.SaveScore(ctx, store.Score{
		StreamID: r.ID, ChannelID: r.ChannelID, LastSeenAt: r.LastSeenAt, Version: Version, Minutes: minutes,
		Quotient: q, Confidence: conf, Final: r.Ended, Components: comps,
	})
}

// sharedAccounts finds young or suspended chatters who also chatted in other
// tracked channels during the week before and during the stream. Once a
// stream's chatter rows are pruned, the previously stored result is reused.
func (s *Scorer) sharedAccounts(ctx context.Context, r store.StreamRef, chatters int) (detect.Component, error) {
	pruned, err := s.store.ChattersPruned(ctx, r.ID)
	if err != nil {
		return detect.Component{}, err
	}
	if pruned {
		raw, err := s.store.PreviousComponent(ctx, r.ID, "shared_accounts")
		var c detect.Component
		if err != nil || raw == nil || json.Unmarshal(raw, &c) != nil {
			return detect.Component{Reason: "chatter list pruned"}, nil
		}
		return c, nil
	}
	young, accounts, err := s.store.SharedYoungChatters(ctx, r.ID, r.ChannelID,
		r.StartedAt.Add(-youngAccountAge), r.StartedAt.Add(-sharedWindow), r.LastSeenAt)
	if err != nil {
		return detect.Component{}, err
	}
	others := make([]int, len(accounts))
	perChannel := map[int64]int{}
	for i, a := range accounts {
		others[i] = len(a.Channels)
		if len(a.Channels) >= sharedMinOther {
			for _, ch := range a.Channels {
				perChannel[ch]++
			}
		}
	}
	c := detect.SharedAccounts(chatters, young, others, sharedMinOther)
	if c.Available && len(perChannel) > 0 {
		c.Reason, err = s.sharedNote(ctx, perChannel)
		if err != nil {
			return detect.Component{}, err
		}
	}
	return c, nil
}

// sharedNote names the channels sharing the most flagged accounts.
func (s *Scorer) sharedNote(ctx context.Context, perChannel map[int64]int) (string, error) {
	ids := slices.Collect(maps.Keys(perChannel))
	slices.SortFunc(ids, func(a, b int64) int {
		if d := perChannel[b] - perChannel[a]; d != 0 {
			return d
		}
		return cmp.Compare(a, b)
	})
	ids = ids[:min(5, len(ids))]
	logins, err := s.store.ChannelLogins(ctx, ids)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%s (%d)", logins[id], perChannel[id])
	}
	return "Most shared with " + strings.Join(parts, ", ") + ".", nil
}

// synchrony stores this stream's spike onsets and compares them with every
// other channel live at the same time.
func (s *Scorer) synchrony(ctx context.Context, r store.StreamRef, p detect.Prepared) (detect.Component, error) {
	start := r.FirstSeenAt.Truncate(time.Minute)
	var idx []int
	if !p.Noisy() {
		idx = detect.SpikeOnsets(p, spikeThreshold)
	}
	onsets := make([]store.Onset, len(idx))
	own := make([]int64, len(idx))
	for i, m := range idx {
		ts := start.Add(time.Duration(m) * time.Minute)
		onsets[i] = store.Onset{TS: ts, Rel: p.Rel[m]}
		own[i] = ts.Unix() / 60
	}
	if err := s.store.ReplaceOnsets(ctx, r.ID, r.ChannelID, onsets); err != nil {
		return detect.Component{}, err
	}
	rows, err := s.store.PartnerOnsets(ctx, r.ChannelID, r.FirstSeenAt, r.LastSeenAt)
	if err != nil {
		return detect.Component{}, err
	}
	partners := make([]detect.Partner, len(rows))
	for i, row := range rows {
		mins := make([]int64, len(row.Onsets))
		for j, t := range row.Onsets {
			mins[j] = t.Unix() / 60
		}
		partners[i] = detect.Partner{Login: row.Login, OverlapMinutes: row.OverlapMinutes, Onsets: mins}
	}
	if p.Noisy() {
		return detect.Component{Reason: "Twitch viewer count too erratic between updates"}, nil
	}
	// Helix refreshes every 3-5 minutes, so onsets can be off by a few minutes.
	return detect.Synchrony(own, partners, 3), nil
}

func round(x float64) float64 { return math.Round(x*1000) / 1000 }

// mainGame returns the category with the most viewer samples.
func mainGame(points []store.ViewerPoint) string {
	counts := map[string]int{}
	best, bestN := "", 0
	for _, p := range points {
		if p.GameID == "" {
			continue
		}
		counts[p.GameID]++
		if n := counts[p.GameID]; n > bestN || (n == bestN && p.GameID < best) {
			best, bestN = p.GameID, n
		}
	}
	return best
}

// engagementNote says which peer group a stream's chat was compared with.
func engagementNote(e Expectation) string {
	lang := "all languages"
	if e.PerLanguage {
		lang = "the same language"
	}
	if e.Category == "" {
		return fmt.Sprintf("Compared with streams in %s; category unknown.", lang)
	}
	return fmt.Sprintf("Compared with streams in %s, adjusted for %s (%d recent streams) and similar %s categories (%d).",
		lang, e.Category, e.CategoryPeers, e.Family, e.FamilyPeers)
}
