// Package tracker discovers top channels each minute, polls every tracked
// channel's viewer count, keeps stream sessions up to date and samples
// follower totals.
package tracker

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"git.subcult.tv/subculture-collective/sus-quotient/internal/helix"
	"git.subcult.tv/subculture-collective/sus-quotient/internal/live"
	"git.subcult.tv/subculture-collective/sus-quotient/internal/store"
)

type Config struct {
	TopN int
	// EndGrace is how long a polled channel must be missing from Helix before
	// its stream is closed. Helix occasionally drops a live stream for a poll.
	EndGrace time.Duration
	// ColdEvery is the polling interval, in minutes, for cold-tier channels.
	ColdEvery int
}

type Poller struct {
	cfg   Config
	helix *helix.Client
	store *store.Store
	live  *live.State
	log   *slog.Logger

	followers *followerSampler

	lastOK     atomic.Int64 // unix seconds of the last successful tick
	lastLive   atomic.Int64
	lastPolled atomic.Int64
}

func New(cfg Config, h *helix.Client, s *store.Store, ls *live.State, log *slog.Logger) *Poller {
	return &Poller{
		cfg: cfg, helix: h, store: s, live: ls, log: log,
		followers: newFollowerSampler(h, s, log),
	}
}

// Health reports the time of the last successful tick and its counts.
func (p *Poller) Health() (lastOK time.Time, live, polled int64) {
	return time.Unix(p.lastOK.Load(), 0), p.lastLive.Load(), p.lastPolled.Load()
}

// Init restores open streams from the database so that a restart continues
// existing sessions instead of opening new ones.
func (p *Poller) Init(ctx context.Context) error {
	open, err := p.store.OpenStreams(ctx)
	if err != nil {
		return err
	}
	for _, o := range open {
		p.live.Set(live.Stream{
			StreamID: o.ID, ChannelID: o.ChannelID, Login: o.Login, Title: o.Title,
			GameID: o.GameID, GameName: o.GameName, LastSeen: o.LastSeen,
		})
	}
	p.log.Info("restored open streams", "count", len(open))
	return nil
}

// Run ticks once per minute, a couple of seconds after the minute boundary.
func (p *Poller) Run(ctx context.Context) {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute + 2*time.Second)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		t := next.Truncate(time.Minute)
		tickCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
		start := time.Now()
		if err := p.tick(tickCtx, t); err != nil {
			p.log.Error("poll failed", "minute", t, "err", err)
		} else {
			p.log.Info("poll", "minute", t.Format(time.TimeOnly), "live", p.lastLive.Load(),
				"polled", p.lastPolled.Load(), "took", time.Since(start).Round(time.Millisecond))
		}
		cancel()
	}
}

func (p *Poller) tick(ctx context.Context, t time.Time) error {
	top, err := p.helix.TopStreams(ctx, p.cfg.TopN)
	if err != nil && len(top) == 0 {
		return err
	}
	if err != nil {
		p.log.Warn("partial top list", "got", len(top), "err", err)
	}
	topRank := map[int64]int{}
	entries := make([]store.TopEntry, 0, len(top))
	for i, s := range top {
		id, err := strconv.ParseInt(s.UserID, 10, 64)
		if err != nil {
			continue
		}
		topRank[id] = i + 1
		entries = append(entries, store.TopEntry{ID: id, Login: s.UserLogin, DisplayName: s.UserName, Rank: i + 1})
	}
	if err := p.store.UpsertTopChannels(ctx, t, entries); err != nil {
		return err
	}
	if err := p.store.DemoteHot(ctx, t); err != nil {
		return err
	}
	chans, err := p.store.Channels(ctx)
	if err != nil {
		return err
	}
	tiers := make(map[int64]string, len(chans))
	polled := map[int64]bool{}
	var due []string
	for _, c := range chans {
		tiers[c.ID] = c.Tier
		if _, ok := topRank[c.ID]; ok {
			polled[c.ID] = true
			continue
		}
		if c.Tier == "cold" && (t.Minute()%p.cfg.ColdEvery) != int(c.ID%int64(p.cfg.ColdEvery)) {
			continue
		}
		polled[c.ID] = true
		due = append(due, strconv.FormatInt(c.ID, 10))
	}
	rest, err := p.helix.StreamsByUserIDs(ctx, due)
	if err != nil {
		// Without a complete poll we can't tell offline from unpolled; keep
		// what we got but don't close any streams this minute.
		p.log.Warn("partial tracked poll", "got", len(rest), "err", err)
		polled = map[int64]bool{}
		for id := range topRank {
			polled[id] = true
		}
	}

	samples := make([]store.LiveSample, 0, len(top)+len(rest))
	for _, s := range append(top, rest...) {
		if s.Type != "live" {
			continue
		}
		cid, err1 := strconv.ParseInt(s.UserID, 10, 64)
		sid, err2 := strconv.ParseInt(s.ID, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		samples = append(samples, store.LiveSample{
			StreamID: sid, ChannelID: cid, Login: s.UserLogin, Display: s.UserName, StartedAt: s.StartedAt,
			Title: s.Title, GameID: s.GameID, GameName: s.GameName, Language: s.Language,
			Viewers: s.ViewerCount, Rank: topRank[cid],
		})
	}
	if err := p.store.RecordLive(ctx, t, samples); err != nil {
		return err
	}

	var events []store.Event
	seen := map[int64]bool{}
	for _, s := range samples {
		seen[s.ChannelID] = true
		prev, ok := p.live.Get(s.ChannelID)
		switch {
		case !ok || prev.StreamID != s.StreamID:
			if ok {
				if err := p.store.EndStream(ctx, prev.StreamID, prev.LastSeen); err != nil {
					return err
				}
				events = append(events, store.Event{TS: prev.LastSeen, ChannelID: s.ChannelID, StreamID: prev.StreamID, Kind: "stream_end"})
			}
			events = append(events, store.Event{TS: t, ChannelID: s.ChannelID, StreamID: s.StreamID, Kind: "stream_start",
				Detail: map[string]any{"started_at": s.StartedAt, "title": s.Title, "game": s.GameName}})
		default:
			if prev.Title != s.Title {
				events = append(events, store.Event{TS: t, ChannelID: s.ChannelID, StreamID: s.StreamID, Kind: "title_change",
					Detail: map[string]any{"from": prev.Title, "to": s.Title}})
			}
			if prev.GameID != s.GameID {
				events = append(events, store.Event{TS: t, ChannelID: s.ChannelID, StreamID: s.StreamID, Kind: "game_change",
					Detail: map[string]any{"from": prev.GameName, "to": s.GameName}})
			}
		}
		p.live.Set(live.Stream{
			StreamID: s.StreamID, ChannelID: s.ChannelID, Login: s.Login, Tier: tiers[s.ChannelID],
			Title: s.Title, GameID: s.GameID, GameName: s.GameName, Viewers: s.Viewers, Rank: s.Rank, LastSeen: t,
		})
	}
	for _, st := range p.live.All() {
		if seen[st.ChannelID] || !polled[st.ChannelID] {
			continue
		}
		if t.Sub(st.LastSeen) < p.cfg.EndGrace {
			continue
		}
		if err := p.store.EndStream(ctx, st.StreamID, st.LastSeen); err != nil {
			return err
		}
		events = append(events, store.Event{TS: st.LastSeen, ChannelID: st.ChannelID, StreamID: st.StreamID, Kind: "stream_end"})
		p.live.Delete(st.ChannelID)
	}
	if err := p.store.InsertEvents(ctx, events); err != nil {
		return err
	}

	p.lastOK.Store(time.Now().Unix())
	p.lastLive.Store(int64(len(samples)))
	p.lastPolled.Store(int64(len(polled)))
	p.followers.trigger(ctx, t, chans, p.live)
	return nil
}

// followerSampler polls follower totals on a per-tier cadence. Each minute's
// run is bounded so it never overlaps the next one.
type followerSampler struct {
	helix   *helix.Client
	store   *store.Store
	log     *slog.Logger
	running atomic.Bool
	mu      sync.Mutex
	nextDue map[int64]time.Time
}

func newFollowerSampler(h *helix.Client, s *store.Store, log *slog.Logger) *followerSampler {
	return &followerSampler{helix: h, store: s, log: log, nextDue: map[int64]time.Time{}}
}

func followerInterval(tier string, isLive bool) time.Duration {
	switch {
	case isLive && tier == "hot":
		return time.Minute
	case isLive && tier == "tracked":
		return 2 * time.Minute
	case isLive:
		return 10 * time.Minute
	case tier == "cold":
		return 6 * time.Hour
	default:
		return time.Hour
	}
}

func (f *followerSampler) trigger(parent context.Context, t time.Time, chans []store.Channel, ls *live.State) {
	if !f.running.CompareAndSwap(false, true) {
		f.log.Warn("follower sampling still running; skipping minute")
		return
	}
	var due []int64
	f.mu.Lock()
	for _, c := range chans {
		_, isLive := ls.Get(c.ID)
		iv := followerInterval(c.Tier, isLive)
		next, ok := f.nextDue[c.ID]
		if !ok {
			next = t
			if !isLive {
				// Spread the first offline samples across the interval.
				next = t.Add(time.Duration(c.ID%int64(iv/time.Minute)) * time.Minute)
			}
		}
		// A channel that just went live may still be on its slower offline schedule.
		if isLive && next.Sub(t) > iv {
			next = t
		}
		if t.Before(next) {
			f.nextDue[c.ID] = next
			continue
		}
		due = append(due, c.ID)
		f.nextDue[c.ID] = t.Add(iv)
	}
	f.mu.Unlock()

	go func() {
		defer f.running.Store(false)
		ctx, cancel := context.WithTimeout(parent, 55*time.Second)
		defer cancel()
		jobs := make(chan int64)
		var (
			mu      sync.Mutex
			samples []store.FollowerSample
			wg      sync.WaitGroup
		)
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for id := range jobs {
					total, err := f.helix.FollowerTotal(ctx, strconv.FormatInt(id, 10))
					if err != nil {
						if ctx.Err() == nil {
							f.log.Debug("follower total", "channel", id, "err", err)
						}
						// Retry next minute instead of waiting a full interval.
						f.mu.Lock()
						f.nextDue[id] = t.Add(time.Minute)
						f.mu.Unlock()
						continue
					}
					mu.Lock()
					samples = append(samples, store.FollowerSample{ChannelID: id, Total: total})
					mu.Unlock()
				}
			}()
		}
	feed:
		for _, id := range due {
			select {
			case jobs <- id:
			case <-ctx.Done():
				break feed
			}
		}
		close(jobs)
		wg.Wait()
		if err := f.store.InsertFollowerSamples(context.WithoutCancel(ctx), t, samples); err != nil {
			f.log.Error("insert follower samples", "err", err)
		}
		if len(due) > 0 {
			f.log.Debug("followers", "due", len(due), "sampled", len(samples))
		}
	}()
}
