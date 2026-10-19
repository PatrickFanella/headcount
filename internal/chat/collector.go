// Package chat reads chat for live tracked channels over anonymous IRC and
// writes per-minute aggregates. Message text is only held in memory for the
// current minute, to measure repetition.
package chat

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	twitch "github.com/gempir/go-twitch-irc/v4"

	"git.subcult.tv/subculture-collective/sus-quotient/internal/helix"
	"git.subcult.tv/subculture-collective/sus-quotient/internal/live"
	"git.subcult.tv/subculture-collective/sus-quotient/internal/store"
)

const (
	channelsPerConn = 80
	maxPendingUsers = 200_000
	maxTextLen      = 200
)

type minuteAgg struct {
	messages, newChatters, firstMsgs, emoteOnly, bans, timeouts int
	chatters                                                    map[int64]int
	texts                                                       map[string]int
}

func newMinuteAgg() *minuteAgg {
	return &minuteAgg{chatters: map[int64]int{}, texts: map[string]int{}}
}

type streamSeen struct {
	streamID int64
	users    map[int64]struct{}
}

type conn struct {
	client   *twitch.Client
	channels map[string]bool
}

type Collector struct {
	live  *live.State
	store *store.Store
	helix *helix.Client
	log   *slog.Logger

	mu        sync.Mutex
	aggs      map[int64]*minuteAgg
	seen      map[int64]*streamSeen
	roomState map[int64]map[string]int
	events    []store.Event
	pending   map[int64]struct{}

	joinMu sync.Mutex
	conns  []*conn
	joined map[string]*conn

	statsMu      sync.Mutex
	totalMsgs    int64
	lastFlush    time.Time
	lastMessages int
}

func New(ls *live.State, s *store.Store, h *helix.Client, log *slog.Logger) *Collector {
	twitch.ReadBufferSize = 8192
	return &Collector{
		live: ls, store: s, helix: h, log: log,
		aggs:      map[int64]*minuteAgg{},
		seen:      map[int64]*streamSeen{},
		roomState: map[int64]map[string]int{},
		pending:   map[int64]struct{}{},
		joined:    map[string]*conn{},
	}
}

// Health reports the number of joined channels, the time of the last flush and
// the messages in that minute.
func (c *Collector) Health() (joined int, lastFlush time.Time, lastMessages int) {
	c.joinMu.Lock()
	joined = len(c.joined)
	c.joinMu.Unlock()
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	return joined, c.lastFlush, c.lastMessages
}

func (c *Collector) Run(ctx context.Context) {
	go c.syncLoop(ctx)
	go c.userLoop(ctx)
	c.flushLoop(ctx)
}

func (c *Collector) newConn() *conn {
	client := twitch.NewClient(fmt.Sprintf("justinfan%d", 10000+rand.IntN(89999)), "oauth:anonymous")
	client.Capabilities = []string{twitch.TagsCapability, twitch.CommandsCapability}
	client.OnPrivateMessage(c.onPrivmsg)
	client.OnUserNoticeMessage(c.onUserNotice)
	client.OnClearChatMessage(c.onClearChat)
	client.OnRoomStateMessage(c.onRoomState)
	cn := &conn{client: client, channels: map[string]bool{}}
	go func() {
		backoff := time.Second
		for {
			start := time.Now()
			err := client.Connect()
			if err == twitch.ErrClientDisconnected {
				return
			}
			if time.Since(start) > time.Minute {
				backoff = time.Second
			}
			c.log.Warn("irc connection closed; reconnecting", "err", err, "in", backoff)
			time.Sleep(backoff)
			backoff = min(backoff*2, time.Minute)
		}
	}()
	c.conns = append(c.conns, cn)
	return cn
}

// syncLoop joins chat for live non-cold channels and leaves chat for channels
// that went offline.
func (c *Collector) syncLoop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		c.sync()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Collector) sync() {
	desired := map[string]bool{}
	for _, st := range c.live.All() {
		if st.Tier != "cold" && st.Login != "" {
			desired[strings.ToLower(st.Login)] = true
		}
	}
	c.joinMu.Lock()
	defer c.joinMu.Unlock()
	var joins, parts int
	for login, cn := range c.joined {
		if !desired[login] {
			cn.client.Depart(login)
			delete(cn.channels, login)
			delete(c.joined, login)
			parts++
		}
	}
	for login := range desired {
		if _, ok := c.joined[login]; ok {
			continue
		}
		var target *conn
		for _, cn := range c.conns {
			if len(cn.channels) < channelsPerConn {
				target = cn
				break
			}
		}
		if target == nil {
			target = c.newConn()
		}
		target.client.Join(login)
		target.channels[login] = true
		c.joined[login] = target
		joins++
	}
	c.mu.Lock()
	for room := range c.seen {
		if c.live.StreamID(room) == 0 {
			delete(c.seen, room)
		}
	}
	c.mu.Unlock()
	if joins+parts > 0 {
		c.log.Info("chat sync", "joined", joins, "parted", parts, "channels", len(c.joined), "conns", len(c.conns))
	}
}

func normalize(text string) string {
	s := strings.ToLower(strings.Join(strings.Fields(text), " "))
	if len(s) > maxTextLen {
		s = s[:maxTextLen]
	}
	return s
}

func emoteOnly(msg twitch.PrivateMessage) bool {
	if len(msg.Emotes) == 0 {
		return false
	}
	names := make(map[string]bool, len(msg.Emotes))
	for _, e := range msg.Emotes {
		names[e.Name] = true
	}
	for _, w := range strings.Fields(msg.Message) {
		if !names[w] {
			return false
		}
	}
	return true
}

// agg returns the current minute's aggregate for a channel. Caller holds c.mu.
func (c *Collector) agg(channelID int64) *minuteAgg {
	a, ok := c.aggs[channelID]
	if !ok {
		a = newMinuteAgg()
		c.aggs[channelID] = a
	}
	return a
}

func (c *Collector) onPrivmsg(msg twitch.PrivateMessage) {
	room, err1 := strconv.ParseInt(msg.RoomID, 10, 64)
	user, err2 := strconv.ParseInt(msg.User.ID, 10, 64)
	if err1 != nil || err2 != nil {
		return
	}
	// Shared chat delivers other channels' messages; count them only where sent.
	if msg.Source != nil && msg.Source.RoomID != "" && msg.Source.RoomID != msg.RoomID {
		return
	}
	streamID := c.live.StreamID(room)
	text := normalize(msg.Message)
	eo := emoteOnly(msg)

	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.agg(room)
	a.messages++
	a.chatters[user]++
	if text != "" {
		a.texts[text]++
	}
	if msg.FirstMessage {
		a.firstMsgs++
	}
	if eo {
		a.emoteOnly++
	}
	ss := c.seen[room]
	if ss == nil || ss.streamID != streamID {
		ss = &streamSeen{streamID: streamID, users: map[int64]struct{}{}}
		c.seen[room] = ss
		if streamID != 0 {
			go c.loadSeen(room, streamID)
		}
	}
	if _, ok := ss.users[user]; !ok {
		ss.users[user] = struct{}{}
		a.newChatters++
		if len(c.pending) < maxPendingUsers {
			c.pending[user] = struct{}{}
		}
	}
}

// loadSeen merges chatters already stored for a stream, so a restart doesn't
// count every returning chatter as new.
func (c *Collector) loadSeen(room, streamID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ids, err := c.store.StreamChatterIDs(ctx, streamID)
	if err != nil {
		c.log.Warn("load stream chatters", "stream", streamID, "err", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ss := c.seen[room]; ss != nil && ss.streamID == streamID {
		for _, id := range ids {
			ss.users[id] = struct{}{}
		}
	}
}

func (c *Collector) onUserNotice(msg twitch.UserNoticeMessage) {
	if msg.MsgID != "raid" {
		return
	}
	room, err := strconv.ParseInt(msg.RoomID, 10, 64)
	if err != nil {
		return
	}
	viewers, _ := strconv.Atoi(msg.MsgParams["msg-param-viewerCount"])
	ev := store.Event{TS: time.Now().UTC(), ChannelID: room, StreamID: c.live.StreamID(room), Kind: "raid_in",
		Detail: map[string]any{"from": msg.MsgParams["msg-param-login"], "from_id": msg.User.ID, "viewers": viewers}}
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *Collector) onClearChat(msg twitch.ClearChatMessage) {
	room, err := strconv.ParseInt(msg.RoomID, 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case msg.TargetUserID == "":
		c.events = append(c.events, store.Event{TS: time.Now().UTC(), ChannelID: room, StreamID: c.live.StreamID(room), Kind: "chat_cleared"})
	case msg.BanDuration == 0:
		c.agg(room).bans++
	default:
		c.agg(room).timeouts++
	}
}

func (c *Collector) onRoomState(msg twitch.RoomStateMessage) {
	room, err := strconv.ParseInt(msg.RoomID, 10, 64)
	if err != nil || len(msg.State) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.roomState[room]
	next := maps.Clone(prev)
	if next == nil {
		next = map[string]int{}
	}
	maps.Copy(next, msg.State)
	if maps.Equal(prev, next) {
		return
	}
	c.roomState[room] = next
	detail := map[string]any{}
	for k, v := range next {
		detail[k] = v
	}
	c.events = append(c.events, store.Event{TS: time.Now().UTC(), ChannelID: room, StreamID: c.live.StreamID(room), Kind: "chat_mode", Detail: detail})
}

func (c *Collector) flushLoop(ctx context.Context) {
	for {
		next := time.Now().Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		c.flush(ctx, next.Add(-time.Minute))
	}
}

func (c *Collector) flush(ctx context.Context, minute time.Time) {
	c.mu.Lock()
	aggs := c.aggs
	events := c.events
	c.aggs = map[int64]*minuteAgg{}
	c.events = nil
	c.mu.Unlock()

	var (
		minutes  []store.ChatMinute
		chatters []store.ChatterMinute
		total    int
	)
	for room, a := range aggs {
		sid := c.live.StreamID(room)
		top := 0
		for _, n := range a.texts {
			top = max(top, n)
		}
		total += a.messages
		minutes = append(minutes, store.ChatMinute{
			ChannelID: room, StreamID: sid, Messages: a.messages, UniqueChatters: len(a.chatters),
			NewChatters: a.newChatters, FirstMsgs: a.firstMsgs, EmoteOnly: a.emoteOnly,
			DistinctTexts: len(a.texts), TopTextCount: top, Bans: a.bans, Timeouts: a.timeouts,
		})
		if sid == 0 {
			continue
		}
		for user, n := range a.chatters {
			chatters = append(chatters, store.ChatterMinute{StreamID: sid, UserID: user, ChannelID: room, Messages: n})
		}
	}
	wctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := c.store.RecordChat(wctx, minute, minutes, chatters); err != nil {
		c.log.Error("record chat", "minute", minute, "err", err)
	}
	if err := c.store.InsertEvents(wctx, events); err != nil {
		c.log.Error("insert chat events", "err", err)
	}
	c.statsMu.Lock()
	c.totalMsgs += int64(total)
	c.lastFlush = time.Now()
	c.lastMessages = total
	c.statsMu.Unlock()
	c.log.Info("chat minute", "minute", minute.Format(time.TimeOnly), "channels", len(minutes),
		"messages", total, "chatter_rows", len(chatters), "events", len(events))
}

// userLoop looks up account creation dates for newly seen chatters.
func (c *Collector) userLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		batch := make([]int64, 0, 2000)
		for id := range c.pending {
			batch = append(batch, id)
			delete(c.pending, id)
			if len(batch) == cap(batch) {
				break
			}
		}
		c.mu.Unlock()
		if len(batch) == 0 {
			continue
		}
		if err := c.lookupUsers(ctx, batch); err != nil && ctx.Err() == nil {
			c.log.Warn("user lookup", "err", err)
		}
	}
}

func (c *Collector) lookupUsers(ctx context.Context, ids []int64) error {
	unknown, err := c.store.UnknownUsers(ctx, ids)
	if err != nil {
		return err
	}
	for start := 0; start < len(unknown); start += 100 {
		chunk := unknown[start:min(start+100, len(unknown))]
		strIDs := make([]string, len(chunk))
		for i, id := range chunk {
			strIDs[i] = strconv.FormatInt(id, 10)
		}
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		found, err := c.helix.Users(lctx, strIDs)
		cancel()
		if err != nil {
			// Put the rest back for a later attempt.
			c.mu.Lock()
			for _, id := range unknown[start:] {
				c.pending[id] = struct{}{}
			}
			c.mu.Unlock()
			return err
		}
		byID := map[int64]helix.User{}
		for _, u := range found {
			if id, err := strconv.ParseInt(u.ID, 10, 64); err == nil {
				byID[id] = u
			}
		}
		rows := make([]store.TwitchUser, 0, len(chunk))
		for _, id := range chunk {
			if u, ok := byID[id]; ok {
				created := u.CreatedAt
				rows = append(rows, store.TwitchUser{ID: id, Login: u.Login, CreatedAt: &created})
			} else {
				rows = append(rows, store.TwitchUser{ID: id})
			}
		}
		if err := c.store.InsertUsers(ctx, rows); err != nil {
			return err
		}
	}
	return nil
}
