package telegram

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Decision is the operator's answer to a tool-approval prompt.
type Decision string

const (
	DecisionApprove        Decision = "approve"
	DecisionApproveSession Decision = "approve_session"
	DecisionDeny           Decision = "deny"
	DecisionCancelTurn     Decision = "cancel_turn"
	DecisionTimeout        Decision = "timeout"
)

// pendingApproval tracks a single in-flight Bash approval for a chat.
// The result channel is buffered (capacity 1) so the resolving callback
// never blocks on a slow driver goroutine.
type pendingApproval struct {
	chatID    int64
	opID      string
	command   string
	messageID int // Telegram message holding the approval keyboard
	result    chan Decision
}

// approvalStore is a concurrency-safe map keyed by opID. Entries are
// created by the turn driver and consumed by the callback handler (or
// dropped by the driver on timeout).
type approvalStore struct {
	mu      sync.Mutex
	entries map[string]*pendingApproval
}

func newApprovalStore() *approvalStore {
	return &approvalStore{entries: map[string]*pendingApproval{}}
}

// Register creates a new pending approval with a fresh opID. The caller
// should store messageID once the keyboard has been sent (via setMessageID).
func (s *approvalStore) Register(chatID int64, command string) *pendingApproval {
	p := &pendingApproval{
		chatID:  chatID,
		opID:    newOpID(),
		command: command,
		result:  make(chan Decision, 1),
	}
	s.mu.Lock()
	s.entries[p.opID] = p
	s.mu.Unlock()
	return p
}

// Resolve delivers the operator's decision to the waiting driver. Safe to
// call with an unknown opID (no-op). Returns the pending entry for the
// caller to edit the approval message.
func (s *approvalStore) Resolve(opID string, d Decision) (*pendingApproval, bool) {
	s.mu.Lock()
	p, ok := s.entries[opID]
	if ok {
		delete(s.entries, opID)
	}
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	// Non-blocking send: the receiver uses a buffered channel of cap 1.
	select {
	case p.result <- d:
	default:
	}
	return p, true
}

// Forget removes an entry without sending on its result channel. Used by the
// driver on timeout or cleanup paths.
func (s *approvalStore) Forget(opID string) {
	s.mu.Lock()
	delete(s.entries, opID)
	s.mu.Unlock()
}

func (p *pendingApproval) setMessageID(id int) {
	p.messageID = id
}

// newOpID returns an 8-byte random identifier hex-encoded (16 chars).
func newOpID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
