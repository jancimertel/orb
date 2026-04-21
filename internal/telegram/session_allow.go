package telegram

import "sync"

// sessionAllowStore remembers "allow Bash for the rest of this session"
// decisions per chat. A flag persists until the chat explicitly clears it
// (e.g. /new, /cancel, /model) since those tear down the runner.
//
// Hard-deny rules still apply even when the flag is set — they are checked
// before this store in Decide.
type sessionAllowStore struct {
	mu  sync.Mutex
	set map[int64]struct{}
}

func newSessionAllowStore() *sessionAllowStore {
	return &sessionAllowStore{set: map[int64]struct{}{}}
}

func (s *sessionAllowStore) Set(chatID int64) {
	s.mu.Lock()
	s.set[chatID] = struct{}{}
	s.mu.Unlock()
}

func (s *sessionAllowStore) IsSet(chatID int64) bool {
	s.mu.Lock()
	_, ok := s.set[chatID]
	s.mu.Unlock()
	return ok
}

func (s *sessionAllowStore) Clear(chatID int64) {
	s.mu.Lock()
	delete(s.set, chatID)
	s.mu.Unlock()
}
