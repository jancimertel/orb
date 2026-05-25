package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"path/filepath"

	"github.com/joho/godotenv"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/jancimertel/orb/internal/agent"
	"github.com/jancimertel/orb/internal/approval"
	"github.com/jancimertel/orb/internal/auth"
	"github.com/jancimertel/orb/internal/claude"
	"github.com/jancimertel/orb/internal/config"
	"github.com/jancimertel/orb/internal/repo"
	"github.com/jancimertel/orb/internal/session"
	"github.com/jancimertel/orb/internal/state"
	"github.com/jancimertel/orb/internal/telegram"
	"github.com/jancimertel/orb/internal/usage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Best-effort: load .env from cwd so `go run ./cmd/bot` works without a
	// wrapper. Docker deployments inject env directly and won't have this
	// file — missing is not an error.
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel, cfg.LogFormat)
	logger.Info("orb starting",
		"model", cfg.DefaultModel,
		"allowed_user_id", cfg.AllowedUserID,
		"db_path", cfg.DBPath,
	)

	// Bootstrap git identity before anything spawns git commands. Writes
	// to HOME/.gitconfig, so it must happen after HOME is effectively set
	// (HomeDir env is applied per-subprocess by the Claude runner, but
	// the bot-process gitconfig is also written here for the Manager's
	// own git invocations).
	gitCtx, gitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := repo.SetGlobalUserConfig(gitCtx, cfg.GitUserName, cfg.GitUserEmail); err != nil {
		gitCancel()
		return fmt.Errorf("git config: %w", err)
	}
	if err := repo.InstallCredentialHelper(gitCtx, cfg.HomeDir); err != nil {
		gitCancel()
		return fmt.Errorf("git credential helper: %w", err)
	}
	gitCancel()

	// Mirror GITHUB_PAT into GH_TOKEN so the `gh` CLI inside Claude
	// subprocesses authenticates without a separate `gh auth login`. The
	// Claude runner inherits os.Environ() for each spawn, so setting this
	// here is enough.
	os.Setenv("GH_TOKEN", cfg.GithubPAT)

	if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
		return fmt.Errorf("workspace: mkdir: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return fmt.Errorf("state: mkdir: %w", err)
	}
	store, err := state.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("state close error", "err", err)
		}
	}()

	bot, err := telego.NewBot(cfg.TelegramBotToken, telego.WithDefaultLogger(false, false))
	if err != nil {
		return fmt.Errorf("telegram: new bot: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	updates, err := bot.UpdatesViaLongPolling(ctx, nil)
	if err != nil {
		return fmt.Errorf("telegram: long polling: %w", err)
	}

	handler, err := th.NewBotHandler(bot, updates)
	if err != nil {
		return fmt.Errorf("telegram: new bot handler: %w", err)
	}

	handler.Use(
		th.PanicRecovery(),
		auth.AllowList(cfg.AllowedUserID, logger),
	)

	registry := claude.NewRegistry(claude.RegistryConfig{
		CLIPath:        cfg.ClaudeCLI,
		HomeDir:        cfg.HomeDir,
		APIKey:         cfg.AnthropicAPIKey,
		ScratchDir:     cfg.ScratchDir(),
		DefaultModel:   cfg.DefaultModel,
		HookBinary:     cfg.ApprovalHookBin,
		ApprovalSocket: cfg.ApprovalSocket,
		Logger:         logger,
	})
	defer registry.Shutdown()

	usageTracker := usage.NewTracker(store)
	sessionLister := session.NewLister(cfg.HomeDir)
	repoManager := repo.NewManager(repo.Config{
		Root:   cfg.WorkspaceRoot,
		PAT:    cfg.GithubPAT,
		Store:  store,
		Logger: logger,
	})
	agentLoader := agent.NewLoader(cfg.HomeDir)

	// Seed the persistent .claude tree from the image-baked examples when
	// each subdir is empty. Preserves operator customizations: any existing
	// .md file in a subdir disables seeding for that subdir.
	if _, err := agent.Seed(cfg.ExamplesDir, filepath.Join(cfg.HomeDir, ".claude"), logger); err != nil {
		logger.Warn("seed examples failed", "err", err)
	}

	// Install and enable the configured Claude Code plugins onto the
	// persistent .claude volume. Runs at startup (not build) because plugins
	// live under $HOME/.claude/plugins, which is the claude-home volume that
	// shadows image-baked content, and the official marketplace must be added
	// from its GitHub source (the reserved name rejects local paths). Fetches
	// over the network on first boot only. Non-fatal: the bot starts regardless.
	if err := claude.EnsurePlugins(claude.PluginConfig{
		CLIPath:           cfg.ClaudeCLI,
		HomeDir:           cfg.HomeDir,
		MarketplaceSource: cfg.PluginMarketplaceSource,
		Plugins:           cfg.EnabledPlugins,
		Logger:            logger,
	}); err != nil {
		logger.Warn("ensure plugins failed", "err", err)
	}

	router := telegram.NewRouter(bot, store, registry, usageTracker, sessionLister, repoManager, agentLoader, cfg, logger)
	router.Register(handler)

	cmdsCtx, cmdsCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := router.PublishCommands(cmdsCtx); err != nil {
		logger.Warn("publish bot commands failed", "err", err)
	}
	cmdsCancel()

	approvalSrv := approval.NewServer(cfg.ApprovalSocket, router, logger)
	if err := approvalSrv.Start(); err != nil {
		return fmt.Errorf("approval server: %w", err)
	}
	defer approvalSrv.Stop()

	errCh := make(chan error, 1)
	go func() { errCh <- handler.Start() }()

	// Healthcheck: touch /tmp/alive every 30s so the Docker HEALTHCHECK can
	// detect a stuck process via file mtime. Best-effort — a transient
	// write failure only affects the next probe.
	go healthLoop(ctx, logger)

	logger.Info("long-polling started")

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("handler: %w", err)
		}
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopCancel()
	if err := handler.StopWithContext(stopCtx); err != nil {
		logger.Error("handler stop error", "err", err)
	}
	logger.Info("bot stopped")
	return nil
}

// healthLoop writes an aliveness marker to /tmp/alive every 30 seconds.
// Docker's HEALTHCHECK inspects the file's mtime to decide whether the bot
// has gone dark. Stops cleanly on ctx cancellation.
func healthLoop(ctx context.Context, logger *slog.Logger) {
	const (
		healthPath     = "/tmp/alive"
		healthInterval = 30 * time.Second
	)
	touch := func() {
		if err := os.WriteFile(healthPath, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o644); err != nil {
			logger.Warn("healthcheck write failed", "path", healthPath, "err", err)
		}
	}
	touch()
	t := time.NewTicker(healthInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			touch()
		}
	}
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if strings.ToLower(format) == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
