package chat

import "time"

// Deduplication is bounded by both age and count. Empty IDs are not collapsed.
const dedupLimit = 200_000
const dedupTTL = 10 * time.Minute

type messageID struct {
	id string
	at time.Time
}
type messageIDs struct {
	ids   map[string]time.Time
	queue []messageID
	head  int
}

func (d *messageIDs) seen(id string, now time.Time) bool {
	if id == "" {
		return false
	}
	if d.ids == nil {
		d.ids = map[string]time.Time{}
	}
	for d.head < len(d.queue) && (now.Sub(d.queue[d.head].at) > dedupTTL || len(d.ids) >= dedupLimit) {
		x := d.queue[d.head]
		delete(d.ids, x.id)
		d.head++
	}
	if d.head > 4096 {
		d.queue = append([]messageID(nil), d.queue[d.head:]...)
		d.head = 0
	}
	if _, ok := d.ids[id]; ok {
		return true
	}
	d.ids[id] = now
	d.queue = append(d.queue, messageID{id, now})
	return false
}

type authorBehavior struct {
	last                        time.Time
	commands, utility, botBadge int
	gaps                        [4]int64
}

func (b *authorBehavior) observe(at time.Time) {
	if !b.last.IsZero() && !at.Before(b.last) {
		gap := at.Sub(b.last)
		idx := 3
		switch {
		case gap < time.Second:
			idx = 0
		case gap < 5*time.Second:
			idx = 1
		case gap < 15*time.Second:
			idx = 2
		}
		b.gaps[idx]++
	}
	if at.After(b.last) {
		b.last = at
	}
}

func (c *Collector) requeue(ids []int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		if len(c.pending) >= maxPendingUsers {
			break
		}
		c.pending[id] = struct{}{}
	}
}
