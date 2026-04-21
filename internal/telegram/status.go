package telegram

import (
	"sync"
	"time"
)

// statusTracker keeps the small amount of process-wide state that /status
// surfaces: when the bot came up and a one-slot "last error" per chat.
type statusTracker struct {
	startedAt time.Time

	mu      sync.Mutex
	lastErr map[int64]chatError
}

type chatError struct {
	at      time.Time
	source  string
	message string
}

func newStatusTracker() *statusTracker {
	return &statusTracker{
		startedAt: time.Now(),
		lastErr:   map[int64]chatError{},
	}
}

// RecordError overwrites any previous "last error" for chatID. source is a
// short tag (e.g. "runner", "git push") shown to the operator.
func (s *statusTracker) RecordError(chatID int64, source, message string) {
	if s == nil || message == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr[chatID] = chatError{at: time.Now(), source: source, message: message}
}

// LastError returns the most recent error recorded for chatID.
func (s *statusTracker) LastError(chatID int64) (chatError, bool) {
	if s == nil {
		return chatError{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lastErr[chatID]
	return e, ok
}

// Uptime returns how long the bot has been running.
func (s *statusTracker) Uptime() time.Duration { return time.Since(s.startedAt) }
