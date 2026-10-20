// Package score turns recorded stream data into suspicion scores. Finished
// streams get a final score; live streams get a provisional one every
// LiveEvery so the dashboard can show current values.
package score

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/detect"
	"git.subcult.tv/subculture-collective/headcount/internal/store"
)

// Version increments whenever detector logic or weights change, so old
// scores can be told apart and recomputed.
const Version = 9

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

	lastLiveRun time.Time

	// ChatterRetention is how long per-chatter rows are kept after a stream
	// ends; older streams keep only a summary. Zero disables pruning.
	ChatterRetention time.Duration
	ReplayRetention  time.Duration
	lastPrune        time.Time
}

func New(s *store.Store, log *slog.Logger) *Scorer {
	return &Scorer{store: s, log: log, ReplayRetention: 7 * 24 * time.Hour}
}

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
	if (s.ChatterRetention > 0 || s.ReplayRetention > 0) && time.Since(s.lastPrune) > time.Hour {
		s.lastPrune = time.Now()
		if s.ChatterRetention > 0 {
			s.prune(ctx)
		}
		if s.ReplayRetention > 0 {
			if err := s.store.PruneSnapshots(ctx, time.Now().Add(-s.ReplayRetention)); err != nil {
				s.log.Warn("prune snapshots", "err", err)
			}
		}
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
	if err := s.store.PruneObservations(ctx, cutoff); err != nil {
		s.log.Warn("prune replay evidence", "err", err)
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
		RestrictedMinutes: make([]bool, n), ContextMinutes: make([]bool, n), ChatHealthy: make([]bool, n),
	}
	for _, p := range ser.Viewers {
		if i := idx(p.TS); i >= 0 && i < n {
			d.Viewers[i] = float64(p.Viewers)
		}
	}
	for _, p := range ser.Chat {
		if i := idx(p.TS); i >= 0 && i < n {
			if p.Healthy == nil || !*p.Healthy {
				continue
			}
			d.ChatHealthy[i] = p.Healthy != nil && *p.Healthy
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
	// Replay observed state only from its observation onward. Polled state
	// expires after 15 minutes; IRC modes expire with unobserved chat transport.
	events := slices.Clone(ser.Events)
	slices.SortStableFunc(events, func(a, b store.EventPoint) int { return a.TS.Compare(b.TS) })
	for _, e := range events {
		i := max(0, idx(e.TS))
		if i >= n {
			continue
		}
		switch e.Kind {
		case "raid_in":
			d.Raids = append(d.Raids, detect.Raid{Minute: i, Viewers: int(number(e.Detail["viewers"]))})
		case "game_change":
			d.Changes = append(d.Changes, i)
		}
	}
	// Explicit sourced intervals are applied once, not reparsed per minute.
	for _, e := range events {
		if e.Kind != "distribution_context" {
			continue
		}
		source, _ := e.Detail["source"].(string)
		if strings.TrimSpace(source) == "" {
			continue
		}
		until, err := time.Parse(time.RFC3339, fmt.Sprint(e.Detail["until"]))
		if err != nil {
			continue
		}
		for i := max(0, idx(e.TS)); i < n && start.Add(time.Duration(i)*time.Minute).Before(until); i++ {
			d.ContextMinutes[i] = true
		}
	}
	modes := map[string]store.EventPoint{}
	eventIndex := 0
	healthyStart := start
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		if !d.ChatHealthy[i] {
			healthyStart = at.Add(time.Minute)
		}
		for eventIndex < len(events) && !events[eventIndex].TS.After(at) {
			e := events[eventIndex]
			modes[e.Kind] = e
			eventIndex++
		}
		var latest store.EventPoint
		for _, kind := range []string{"chat_mode", "chat_settings"} {
			e, ok := modes[kind]
			if !ok {
				continue
			}
			valid := at.Sub(e.TS) <= 15*time.Minute
			if kind == "chat_mode" {
				valid = d.ChatHealthy[i] && (valid || !e.TS.Before(healthyStart.Add(-time.Minute)))
			}
			if valid && (latest.TS.IsZero() || e.TS.After(latest.TS)) {
				latest = e
			}
		}
		x := latest.Detail
		if latest.Kind == "chat_mode" {
			_, followers := x["followers-only"]
			d.RestrictedMinutes[i] = number(x["subs-only"]) > 0 || number(x["emote-only"]) > 0 || (followers && number(x["followers-only"]) >= 0) || number(x["slow"]) > 0
		} else {
			d.RestrictedMinutes[i] = truth(x["subscriber_mode"]) || truth(x["emote_mode"]) || truth(x["follower_mode"]) || truth(x["slow_mode"])
		}
		if e, ok := modes["shared_chat"]; ok && at.Sub(e.TS) <= 15*time.Minute && truth(e.Detail["active"]) {
			d.ContextMinutes[i] = true
		}
	}

	restricted := 0
	for _, x := range d.RestrictedMinutes {
		if x {
			restricted++
		}
	}
	d.RestrictedChat = restricted > 0

	return d
}

func (s *Scorer) scoreStream(ctx context.Context, r store.StreamRef) error {
	var input Snapshot
	err := s.store.ReadSnapshot(ctx, func(view *store.Store, at time.Time) error {
		fresh, err := view.StreamRef(ctx, r.ID)
		if err != nil {
			return err
		}
		r = fresh
		ser, err := view.LoadSeries(ctx, r)
		if err != nil {
			return err
		}
		peers, err := view.ChatPeerStats(ctx, r.StartedAt.Add(-7*24*time.Hour), r.StartedAt, r.ChannelID)
		if err != nil {
			return err
		}
		accounts, err := view.ChatterAccounts(ctx, r.ID, r.StartedAt, r.LastSeenAt, newAccountAge)
		if err != nil {
			return err
		}
		behavior, err := view.BehaviorStats(ctx, r.ID)
		if err != nil {
			return err
		}
		reader := *s
		reader.store = view
		shared, err := reader.sharedAccounts(ctx, r, accounts.Total)
		if err != nil {
			return err
		}
		partners, err := reader.synchronyPartners(ctx, r)
		if err != nil {
			return err
		}
		game := mainGame(ser.Viewers)
		name, err := view.GameName(ctx, game)
		if err != nil {
			return err
		}
		behaviorPeers := []store.BehaviorPeer{}
		if stats, ok := detect.ChatEngagement(Build(r, ser), detect.Prepared{}); ok {
			behaviorPeers, err = view.BehaviorPeers(ctx, r, game, stats.MedianViewers)
			if err != nil {
				return err
			}
		}
		input = Snapshot{Schema: 1, ObservedAt: at, Ref: r, Series: ser, Peers: peers, Accounts: accounts, Behavior: behavior, BehaviorPeers: behaviorPeers, Shared: shared, Partners: partners, GameID: game, GameName: name}
		return nil
	})
	if err != nil {
		return err
	}
	output := Evaluate(input, nil)
	d := Build(r, input.Series)
	p := detect.Prepare(d)
	idx := eligibleOnsets(d, p)
	start := r.FirstSeenAt.Truncate(time.Minute)
	onsets := make([]store.Onset, len(idx))
	for i, m := range idx {
		onsets[i] = store.Onset{TS: start.Add(time.Duration(m) * time.Minute), Rel: p.Rel[m]}
	}
	if err := s.store.ReplaceOnsets(ctx, r.ID, r.ChannelID, onsets, Version); err != nil {
		return err
	}
	return s.store.SaveScore(ctx, store.Score{StreamID: r.ID, ChannelID: r.ChannelID, LastSeenAt: r.LastSeenAt, Version: Version, Minutes: len(d.Viewers), Quotient: output.Quotient, Confidence: output.Coverage, Final: r.Ended, Components: output.Components, Evidence: output.Evidence, Peer: output.Peer, Behavior: input.Behavior, SnapshotInput: input, SnapshotOutput: output, ObservedAt: input.ObservedAt})
}

// sharedAccounts describes young authors overlapping tracked channels.
// Pruned legacy results are unavailable rather than reused under new semantics.
func (s *Scorer) sharedAccounts(ctx context.Context, r store.StreamRef, chatters int) (detect.Component, error) {
	pruned, err := s.store.ChattersPruned(ctx, r.ID)
	if err != nil {
		return detect.Component{}, err
	}
	if pruned {
		return detect.Component{Reason: "chatter rows pruned; legacy shared evidence not reused"}, nil
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
	degreeAdjusted := 0.0
	for _, degree := range others {
		if degree >= sharedMinOther {
			degreeAdjusted += 1 / math.Log2(float64(degree)+2)
		}
	}
	if c.Values != nil {
		c.Values["degree_adjusted_overlap"] = degreeAdjusted
	}
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

// synchronyPartners freezes currently available, version-matched partner onsets.
func (s *Scorer) synchronyPartners(ctx context.Context, r store.StreamRef) ([]detect.Partner, error) {
	rows, err := s.store.PartnerOnsets(ctx, r.ChannelID, r.FirstSeenAt, r.LastSeenAt, Version)
	if err != nil {
		return nil, err
	}
	partners := make([]detect.Partner, len(rows))
	for i, row := range rows {
		mins := make([]int64, len(row.Onsets))
		for j, t := range row.Onsets {
			mins[j] = t.Unix() / 60
		}
		partners[i] = detect.Partner{Login: row.Login, OverlapMinutes: row.OverlapMinutes, Onsets: mins}
	}
	return partners, nil
}

func number(x any) float64 {
	switch v := x.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}
func truth(x any) bool { b, _ := x.(bool); return b }

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
