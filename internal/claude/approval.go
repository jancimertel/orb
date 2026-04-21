package claude

import (
	"regexp"
	"strings"
)

// hardDenyRule is a single deny-list pattern plus a human-readable reason
// surfaced to the user and to Claude.
type hardDenyRule struct {
	name    string
	pattern *regexp.Regexp
}

// hardDenyRules must never prompt. They are checked before the approval UI
// is shown and produce an immediate deny response. Patterns are matched
// case-insensitively against the Bash command string.
//
// Keep this list conservative: it should catch clearly-destructive or
// secret-exfiltrating shapes without overreaching. Over-broad rules here
// degrade the approval UX for legitimate commands.
var hardDenyRules = []hardDenyRule{
	// Secret mount and Claude config directory — never let the model touch these.
	{name: "touches /data/secrets", pattern: regexp.MustCompile(`(?i)/data/secrets(?:/|\s|"|'|$)`)},
	{name: "touches /data/.claude", pattern: regexp.MustCompile(`(?i)/data/\.claude(?:/|\s|"|'|$)`)},

	// Recursive root deletes and friends.
	{name: "rm -rf / (or /*)", pattern: regexp.MustCompile(`\brm\s+(-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*|-[a-zA-Z]*f[a-zA-Z]*r[a-zA-Z]*)\s+(/|/\*)(\s|$)`)},
	{name: "mkfs on a device", pattern: regexp.MustCompile(`\bmkfs(\.[a-z0-9]+)?\s+/dev/`)},
	{name: "dd onto /dev", pattern: regexp.MustCompile(`\bdd\b.*\bof=/dev/`)},

	// Classic fork bomb.
	{name: "fork bomb", pattern: regexp.MustCompile(`:\s*\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`)},

	// Exfiltration patterns: piping secrets to the network.
	{name: "curl to pipe", pattern: regexp.MustCompile(`(?i)\bcurl\b[^\n]*\|\s*(sh|bash|zsh|python|perl)\b`)},
	{name: "wget to pipe", pattern: regexp.MustCompile(`(?i)\bwget\b[^\n]*\|\s*(sh|bash|zsh|python|perl)\b`)},

	// Attempts to change permissions on the config/secrets volumes.
	{name: "chmod on /data/secrets", pattern: regexp.MustCompile(`(?i)\bchmod\b.*\s/data/secrets`)},
	{name: "chmod on /data/.claude", pattern: regexp.MustCompile(`(?i)\bchmod\b.*\s/data/\.claude`)},
}

// HardDenyBash returns (reason, true) when the command matches a hard-deny
// rule. The returned reason is safe to surface to the end user and to Claude.
// An empty command is rejected as "empty bash command".
func HardDenyBash(command string) (string, bool) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return "empty bash command", true
	}
	for _, rule := range hardDenyRules {
		if rule.pattern.MatchString(cmd) {
			return rule.name, true
		}
	}
	return "", false
}
