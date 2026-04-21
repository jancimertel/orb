package telegram

import (
	"sync"
	"time"
)

// gitOpKind discriminates pendingGitOp.Args so the executor knows which
// fields to read.
type gitOpKind string

const (
	gitOpBranch   gitOpKind = "branch"
	gitOpCommit   gitOpKind = "commit"
	gitOpPush     gitOpKind = "push"
	gitOpPull     gitOpKind = "pull"
	gitOpCheckout gitOpKind = "checkout"
)

// pendingGitOp tracks a /branch /commit /push /pull request awaiting
// operator confirmation. Entries self-expire after a TTL; the backing
// timer also removes the map entry so we never leak memory.
type pendingGitOp struct {
	chatID    int64
	opID      string
	kind      gitOpKind
	alias     string
	branch    string
	commitMsg string
	messageID int
	timer     *time.Timer
}

type gitConfirmStore struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]*pendingGitOp
}

func newGitConfirmStore(ttl time.Duration) *gitConfirmStore {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &gitConfirmStore{
		ttl:     ttl,
		entries: map[string]*pendingGitOp{},
	}
}

// Register inserts op and arms its expiry timer. Returns the opID so the
// caller can embed it in the callback keyboard. The caller sets
// messageID via SetMessage once the prompt has been sent.
func (s *gitConfirmStore) Register(op *pendingGitOp) string {
	op.opID = newOpID()
	s.mu.Lock()
	s.entries[op.opID] = op
	s.mu.Unlock()
	op.timer = time.AfterFunc(s.ttl, func() {
		s.mu.Lock()
		if cur, ok := s.entries[op.opID]; ok && cur == op {
			delete(s.entries, op.opID)
		}
		s.mu.Unlock()
	})
	return op.opID
}

// Take removes and returns the entry for opID. Returns nil, false when the
// opID is unknown or already expired/consumed.
func (s *gitConfirmStore) Take(opID string) (*pendingGitOp, bool) {
	s.mu.Lock()
	op, ok := s.entries[opID]
	if ok {
		delete(s.entries, opID)
	}
	s.mu.Unlock()
	if ok && op.timer != nil {
		op.timer.Stop()
	}
	return op, ok
}

// SetMessage attaches the Telegram message ID holding the confirm keyboard
// so the callback handler can edit the same message with the outcome.
func (op *pendingGitOp) SetMessage(id int) { op.messageID = id }
