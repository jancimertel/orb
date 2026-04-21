package telegram

import (
	"errors"
	"log/slog"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/agent"
	"github.com/jancimertel/orb/internal/claude"
	"github.com/jancimertel/orb/internal/config"
	"github.com/jancimertel/orb/internal/repo"
	"github.com/jancimertel/orb/internal/session"
	"github.com/jancimertel/orb/internal/state"
	"github.com/jancimertel/orb/internal/usage"
)

// Router wires Telegram commands and text messages to handlers. It is
// constructed once at startup and registered on the telego bot handler.
type Router struct {
	bot         *telego.Bot
	store       *state.Store
	registry    *claude.Registry
	usage       *usage.Tracker
	sessions    *session.Lister
	repos       *repo.Manager
	agents      *agent.Loader
	cfg         *config.Config
	logger      *slog.Logger
	approvals       *approvalStore
	gitConfirms     *gitConfirmStore
	status          *statusTracker
	sessionAllow    *sessionAllowStore
	pendingCommands *pendingCommandStore
	memoryConfirms  *memoryConfirmStore
}

func NewRouter(
	bot *telego.Bot,
	store *state.Store,
	registry *claude.Registry,
	usageTracker *usage.Tracker,
	sessions *session.Lister,
	repos *repo.Manager,
	agents *agent.Loader,
	cfg *config.Config,
	logger *slog.Logger,
) *Router {
	return &Router{
		bot:         bot,
		store:       store,
		registry:    registry,
		usage:       usageTracker,
		sessions:    sessions,
		repos:       repos,
		agents:      agents,
		cfg:         cfg,
		logger:      logger,
		approvals:       newApprovalStore(),
		gitConfirms:     newGitConfirmStore(60 * time.Second),
		status:          newStatusTracker(),
		sessionAllow:    newSessionAllowStore(),
		pendingCommands: newPendingCommandStore(),
		memoryConfirms:  newMemoryConfirmStore(memoryConfirmTTL),
	}
}

// Register attaches all command + fallback handlers to the bot handler.
// Handlers are matched in registration order (first match wins), so the
// generic text fallback is added last.
func (r *Router) Register(h *th.BotHandler) {
	r.registerGeneral(h)
	r.registerSession(h)
	r.registerRepo(h)
	r.registerRuntime(h)
	r.registerAgent(h)
	r.registerCommand(h)
	r.registerSkill(h)
	r.registerMemory(h)
	r.registerContext(h)
	r.registerGit(h)
	r.registerApproval(h)
	r.registerFallback(h)
}

// reply is a small convenience for plain-text replies.
func (r *Router) reply(ctx *th.Context, chatID int64, text string) error {
	_, err := r.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   text,
	})
	if err != nil {
		r.logger.Warn("send message failed", "chat_id", chatID, "err", err)
	}
	return err
}

// stub replies with a "not implemented yet" message tagged with the command.
func (r *Router) stub(cmd string) th.Handler {
	return func(ctx *th.Context, u telego.Update) error {
		if u.Message == nil {
			return nil
		}
		return r.reply(ctx, u.Message.Chat.ID, cmd+" — not implemented yet")
	}
}

// chatStateOrEmpty fetches chat state, returning a zero value if the row does
// not exist yet (i.e. this chat has never set a session/repo/model).
func (r *Router) chatStateOrEmpty(ctx *th.Context, chatID int64) (*state.ChatState, error) {
	cs, err := r.store.GetChatState(ctx, chatID)
	if errors.Is(err, state.ErrNotFound) {
		return &state.ChatState{ChatID: chatID}, nil
	}
	return cs, err
}
