package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	TelegramBotToken string `env:"TELEGRAM_BOT_TOKEN,required,notEmpty"`
	AllowedUserID   int64  `env:"ALLOWED_USER_ID,required,notEmpty"`
	// Optional when signing in with a Pro/Max subscription via `claude login`
	// (OAuth credentials stored in $HOME/.claude). When set, Claude Code
	// prefers the API key over OAuth — so leave it unset to use a subscription.
	AnthropicAPIKey string `env:"ANTHROPIC_API_KEY"`
	GithubPAT       string `env:"GITHUB_PAT,required,notEmpty"`
	GitUserName     string `env:"GIT_USER_NAME,required,notEmpty"`
	GitUserEmail    string `env:"GIT_USER_EMAIL,required,notEmpty"`

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
