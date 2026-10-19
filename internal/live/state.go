// Package live holds the in-memory view of which tracked channels are live,
// shared by the poller, the chat collector and the follower sampler.
package live

import (
	"sync"
	"time"
)

type Stream struct {
	StreamID  int64
	ChannelID int64
	Login     string
	Tier      string
	Title     string
	GameID    string
	GameName  string
	Viewers   int
	Rank      int
	LastSeen  time.Time
}

type State struct {
	mu      sync.RWMutex
	streams map[int64]*Stream // by channel ID
}

func New() *State {
	return &State{streams: map[int64]*Stream{}}
}

func (s *State) Get(channelID int64) (Stream, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.streams[channelID]
	if !ok {
		return Stream{}, false
	}
	return *st, true
}

func (s *State) Set(st Stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := st
	s.streams[st.ChannelID] = &cp
}

func (s *State) Delete(channelID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streams, channelID)
}

// StreamID returns the current stream for a channel, or 0.
func (s *State) StreamID(channelID int64) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.streams[channelID]; ok {
		return st.StreamID
	}
	return 0
}

func (s *State) All() []Stream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Stream, 0, len(s.streams))
	for _, st := range s.streams {
		out = append(out, *st)
	}
	return out
}
