package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	TelegramBotToken string `env:"TELEGRAM_BOT_TOKEN,required,notEmpty"`
	AllowedUserID    int64  `env:"ALLOWED_USER_ID,required,notEmpty"`
	// Optional when signing in with a Pro/Max subscription via `claude login`
	// (OAuth credentials stored in $HOME/.claude). When set, Claude Code
	// prefers the API key over OAuth — so leave it unset to use a subscription.
	AnthropicAPIKey string `env:"ANTHROPIC_API_KEY"`
	// ModelsAPIKey is a READ-ONLY Anthropic console API key used ONLY to list
	// models via GET /v1/models. It is never injected into spawned CLI
	// subprocesses, so model runs stay on the subscription. Empty = use the
	// curated fallback list and make no network calls.
	ModelsAPIKey string `env:"MODELS_API_KEY"`
	GithubPAT    string `env:"GITHUB_PAT,required,notEmpty"`
	GitUserName  string `env:"GIT_USER_NAME,required,notEmpty"`
	GitUserEmail string `env:"GIT_USER_EMAIL,required,notEmpty"`

	DefaultModel       string `env:"DEFAULT_MODEL"                  envDefault:"claude-sonnet-4-6"`
	LogLevel           string `env:"LOG_LEVEL"                      envDefault:"info"`
	LogFormat          string `env:"LOG_FORMAT"                     envDefault:"json"`
	ApprovalTimeoutSec int    `env:"APPROVAL_TIMEOUT_SEC"           envDefault:"60"`
	MaxSessionsListed  int    `env:"MAX_SESSIONS_LISTED"            envDefault:"10"`
	DBPath             string `env:"DB_PATH"                        envDefault:"/data/state/state.db"`
	HomeDir            string `env:"HOME_DIR"                       envDefault:"/data"`
	WorkspaceRoot      string `env:"WORKSPACE_ROOT"                 envDefault:"/data/workspaces"`
	ClaudeCLI          string `env:"CLAUDE_CLI"                     envDefault:"claude"`
	ApprovalSocket     string `env:"APPROVAL_SOCKET"                envDefault:"/tmp/orb-approval.sock"`
	ApprovalHookBin    string `env:"APPROVAL_HOOK_BIN"              envDefault:"/usr/local/bin/orb-approvalhook"`

	// Plugins is the authoritative set of Claude Code plugins the bot installs
	// and enables for spawned subprocesses on startup. Each entry is a
	// "<plugin>@<marketplace>" key. The startup step installs each one and
	// rewrites enabledPlugins in $HOME/.claude/settings.json to match exactly —
	// so removing a plugin here and restarting disables it (only the listed
	// plugins load). Override to add/remove without rebuilding the image — just
	// restart.
	Plugins []string `env:"PLUGINS" envSeparator:"," envDefault:"superpowers@claude-plugins-official,skill-creator@claude-plugins-official,code-simplifier@claude-plugins-official"`

	// PluginMarketplaceSource is the source passed to `claude plugin
	// marketplace add` at startup. The official marketplace name is reserved
	// and only accepts the anthropics GitHub source, so this fetches over the
	// network on first boot (idempotent afterwards).
	PluginMarketplaceSource string `env:"PLUGIN_MARKETPLACE_SOURCE" envDefault:"anthropics/claude-plugins-official"`

	// ExamplesDir points at a read-only `.claude` tree baked into the
	// container image (agents/, commands/, jobs/). On startup the bot seeds
	// any empty subdir under $HOME_DIR/.claude/ from here so a fresh volume
	// has working examples out of the box. Empty = seeding disabled (typical
	// for local dev; the Dockerfile sets it to /opt/bot/examples/.claude).
	ExamplesDir string `env:"EXAMPLES_DIR"`
}

// ScratchDir returns the cwd used when the chat has no repo selected.
func (c *Config) ScratchDir() string { return c.WorkspaceRoot + "/_scratch" }

func Load() (*Config, error) {
	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &cfg, nil
}
