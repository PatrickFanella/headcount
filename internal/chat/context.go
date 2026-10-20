package chat

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/live"
	"git.subcult.tv/subculture-collective/headcount/internal/store"
)

// Results are observations, not exact transition times. Scoring expires them;
// a failed request never means disabled. Low-priority budget protects polling.
func (c *Collector) contextLoop(ctx context.Context) {
	next := map[int64]time.Time{}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		streams := c.live.All()
		slices.SortFunc(streams, func(a, b live.Stream) int { return cmp.Compare(a.ChannelID, b.ChannelID) })
		current := map[int64]bool{}
		budget := 4
		for _, st := range streams {
			current[st.ChannelID] = true
			if st.Tier == "cold" || budget == 0 || time.Now().Before(next[st.ChannelID]) {
				continue
			}
			budget--
			next[st.ChannelID] = time.Now().Add(10 * time.Minute)
			id := strconv.FormatInt(st.ChannelID, 10)
			req, cancel := context.WithTimeout(ctx, 15*time.Second)
			settings, err := c.helix.ChatSettings(req, id)
			cancel()
			if err == nil {
				raw, _ := json.Marshal(settings)
				var detail map[string]any
				_ = json.Unmarshal(raw, &detail)
				c.contextEvent(st, "chat_settings", detail)
			} else {
				c.log.Debug("chat settings unavailable", "channel", st.ChannelID, "err", err)
			}
			req, cancel = context.WithTimeout(ctx, 15*time.Second)
			shared, err := c.helix.SharedChat(req, id)
			cancel()
			if err == nil {
				detail := map[string]any{"active": shared != nil}
				if shared != nil {
					ids := []string{}
					for _, p := range shared.Participants {
						ids = append(ids, p.ID)
					}
					detail["participants"] = ids
					detail["session_id"] = shared.ID
				}
				c.contextEvent(st, "shared_chat", detail)
			} else {
				c.log.Debug("shared chat unavailable", "channel", st.ChannelID, "err", err)
			}
		}
		for id := range next {
			if !current[id] {
				delete(next, id)
			}
		}
	}
}
func (c *Collector) contextEvent(st live.Stream, kind string, detail map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, store.Event{TS: time.Now().UTC(), ChannelID: st.ChannelID, StreamID: st.StreamID, Kind: kind, Detail: detail})
}
