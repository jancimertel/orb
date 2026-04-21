package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionMeta summarizes a single Claude Code session JSONL file.
type SessionMeta struct {
	ID             string
	Path           string
	CWD            string
	StartedAt      time.Time
	LastActivityAt time.Time
	Summary        string
	MessageCount   int
	SizeBytes      int64
}

// Parse reads a session JSONL file and returns its summary metadata.
// The whole file is scanned so MessageCount, last-exchange summary, and
// final timestamp are accurate. Malformed lines are skipped rather than
// aborting the scan.
func Parse(path string) (SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, fmt.Errorf("session: open: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return SessionMeta{}, fmt.Errorf("session: stat: %w", err)
	}

	meta := SessionMeta{
		ID:             strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Path:           path,
		SizeBytes:      info.Size(),
		LastActivityAt: info.ModTime(),
	}

	sc := bufio.NewScanner(f)
	// Session messages can carry large tool_result blobs.
	sc.Buffer(make([]byte, 64*1024), 20*1024*1024)

	var (
		summaryLine       string
		lastUserText      string
		lastAssistantText string
	)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var head lineHead
		if err := json.Unmarshal(line, &head); err != nil {
			continue
		}

		if head.Type == "summary" && head.Summary != "" {
			summaryLine = head.Summary
			continue
		}
		if head.Type != "user" && head.Type != "assistant" {
			continue
		}

		meta.MessageCount++
		if meta.CWD == "" && head.CWD != "" {
			meta.CWD = head.CWD
		}
		if head.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, head.Timestamp); err == nil {
				if meta.StartedAt.IsZero() || t.Before(meta.StartedAt) {
					meta.StartedAt = t
				}
				if t.After(meta.LastActivityAt) {
					meta.LastActivityAt = t
				}
			}
		}
		if head.Message != nil {
			text := extractText(head.Message.Content)
			if text != "" {
				switch head.Message.Role {
				case "user":
					lastUserText = text
				case "assistant":
					lastAssistantText = text
				}
			}
		}
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		return meta, fmt.Errorf("session: scan: %w", err)
	}

	switch {
	case summaryLine != "":
		meta.Summary = truncateRunes(summaryLine, 120)
	default:
		meta.Summary = truncateRunes(buildLastExchangeSummary(lastUserText, lastAssistantText), 120)
	}
	return meta, nil
}

// Delete removes the session JSONL file at path. Returns nil if the file
// does not exist (treated as already-deleted).
func Delete(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("session: remove: %w", err)
	}
	return nil
}

// --- internals ------------------------------------------------------------

// lineHead captures only the fields we need. Unmatched fields are ignored.
type lineHead struct {
	Type      string       `json:"type"`
	Summary   string       `json:"summary,omitempty"`
	CWD       string       `json:"cwd,omitempty"`
	Timestamp string       `json:"timestamp,omitempty"`
	Message   *lineMessage `json:"message,omitempty"`
}

type lineMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// extractText handles message.content being either a plain string or an
// array of content blocks.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" && blk.Text != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

func buildLastExchangeSummary(user, assistant string) string {
	if user == "" && assistant == "" {
		return ""
	}
	var parts []string
	if user != "" {
		parts = append(parts, "Q: "+singleLine(user))
	}
	if assistant != "" {
		parts = append(parts, "A: "+singleLine(assistant))
	}
	return strings.Join(parts, " · ")
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// truncateRunes caps s at n runes, appending "…" when truncated. Operates
// on runes so multibyte text doesn't get cut mid-codepoint.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for idx := range s {
		if i >= n {
			return s[:idx] + "…"
		}
		i++
	}
	return s
}
