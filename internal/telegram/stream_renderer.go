package telegram

import (
	"context"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
)

const (
	placeholderText    = "working…"
	streamEditInterval = 1200 * time.Millisecond
	telegramMsgLimit   = 4000
)

// StreamRenderer edits a single Telegram message in place as Claude events
// arrive. A single background goroutine owns all state and the network calls,
// so caller-side methods are cheap and non-blocking.
//
// Usage:
//
//	sr := NewStreamRenderer(ctx, bot, chatID, logger)
//	sr.AppendText(...)   // from the main goroutine, any number of times
//	sr.AppendTool(...)
//	finalText := sr.Finalize()  // blocks until the final edit + any
//	                            // overflow messages have been sent
type StreamRenderer struct {
	bot    *telego.Bot
	ctx    context.Context
	chatID int64
	logger *slog.Logger

	events chan rendererEvent
	done   chan string
}

type rendererEvent struct {
	kind string
	data string
	tool toolEntry
}

const (
	evText     = "text"
	evTool     = "tool"
	evFinalize = "finalize"
)

// toolEntry is a structured record of a single tool invocation. The renderer
// decides how to format it (grouping, path shortening, etc) at flush time so
// we can make display decisions based on the full batch.
type toolEntry struct {
	name string // "Read", "Edit", "Bash", "Grep", ...
	path string // for file tools; may be absolute
	cmd  string // for Bash
}

// NewStreamRenderer starts the background worker and returns a renderer ready
// to accept Append* calls. The context is used for every outbound Telegram
// call and must outlive the turn.
func NewStreamRenderer(ctx context.Context, bot *telego.Bot, chatID int64, logger *slog.Logger) *StreamRenderer {
	sr := &StreamRenderer{
		bot:    bot,
		ctx:    ctx,
		chatID: chatID,
		logger: logger,
		events: make(chan rendererEvent, 64),
		done:   make(chan string, 1),
	}
	go sr.run()
	return sr
}

// AppendText queues a chunk of assistant text to append to the rendered body.
// Empty strings are ignored. Thread-safe but callers usually invoke from one
// goroutine.
func (sr *StreamRenderer) AppendText(t string) {
	if t == "" {
		return
	}
	sr.events <- rendererEvent{kind: evText, data: t}
}

// AppendTool queues a structured tool-use record. The renderer groups and
// formats runs at flush time.
func (sr *StreamRenderer) AppendTool(t toolEntry) {
	if t.name == "" {
		return
	}
	sr.events <- rendererEvent{kind: evTool, tool: t}
}

// Finalize flushes any pending content, splits long output across messages,
// and blocks until the worker exits. Returns the final rendered assistant
// text (without the tool status prefix) so the caller can record it.
//
// emptyFallback is written to the placeholder if no content was ever
// streamed. Typical values: "(no response)" on clean completion,
// "(cancelled)" when /cancel fired, "(failed)" on crash.
// Call exactly once.
func (sr *StreamRenderer) Finalize(emptyFallback string) string {
	sr.events <- rendererEvent{kind: evFinalize, data: emptyFallback}
	return <-sr.done
}

// run owns all renderer state and serializes every Telegram API call.
func (sr *StreamRenderer) run() {
	var (
		text         strings.Builder
		tools        []toolEntry
		messageID    int
		lastBody     string // last body successfully written to the edited message
		lastParseHTML bool   // parse mode used for the current lastBody
		lastEditAt   time.Time
		pending      bool
	)

	// Timer is always-armed but idle unless we're debouncing an edit.
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	timerArmed := false
	disarmTimer := func() {
		if !timerArmed {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerArmed = false
	}

	render := func() {
		body, isHTML := composeBody(text.String(), tools)
		// HTML mode is only returned when the body already fits; a plaintext
		// body can grow unbounded mid-stream, so cap it at the per-message
		// limit. The final flush in finalizeTurn uses chunkMessage for full
		// output.
		if !isHTML && len(body) > telegramMsgLimit {
			body = body[:telegramMsgLimit-1] + "…"
		}
		if messageID == 0 {
			// Before we've sent anything, a flush emits the placeholder (or
			// the first content if text already arrived before the debounce
			// window closed).
			initial := body
			initialHTML := isHTML
			if initial == "" {
				initial = placeholderText
				initialHTML = false
			}
			params := &telego.SendMessageParams{
				ChatID: telego.ChatID{ID: sr.chatID},
				Text:   initial,
			}
			if initialHTML {
				params.ParseMode = "HTML"
			}
			m, err := sr.bot.SendMessage(sr.ctx, params)
			if err != nil {
				sr.logger.Warn("stream: send placeholder failed", "chat_id", sr.chatID, "err", err)
				pending = false
				return
			}
			messageID = m.MessageID
			lastBody = initial
			lastParseHTML = initialHTML
			lastEditAt = time.Now()
			pending = false
			return
		}
		if body == lastBody && isHTML == lastParseHTML {
			pending = false
			return
		}
		displayBody := body
		displayHTML := isHTML
		if len(displayBody) == 0 {
			displayBody = placeholderText
			displayHTML = false
		}
		params := &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: sr.chatID},
			MessageID: messageID,
			Text:      displayBody,
		}
		if displayHTML {
			params.ParseMode = "HTML"
		}
		_, err := sr.bot.EditMessageText(sr.ctx, params)
		if err != nil {
			sr.logger.Debug("stream: edit failed", "chat_id", sr.chatID, "err", err)
			// Keep lastBody stale so the next flush retries.
		} else {
			lastBody = body
			lastParseHTML = displayHTML
			lastEditAt = time.Now()
		}
		pending = false
	}

	for {
		select {
		case ev := <-sr.events:
			switch ev.kind {
			case evText:
				text.WriteString(ev.data)
				pending = true
			case evTool:
				tools = append(tools, ev.tool)
				pending = true
			case evFinalize:
				disarmTimer()
				sr.finalizeTurn(&text, tools, &messageID, lastBody, lastParseHTML, ev.data)
				sr.done <- text.String()
				return
			}

			if messageID == 0 {
				// Send the placeholder immediately on the first event —
				// immediate feedback trumps the debounce budget.
				render()
				continue
			}
			elapsed := time.Since(lastEditAt)
			if elapsed >= streamEditInterval {
				render()
				disarmTimer()
			} else if !timerArmed {
				timer.Reset(streamEditInterval - elapsed)
				timerArmed = true
			}

		case <-timer.C:
			timerArmed = false
			if pending {
				render()
			}
		}
	}
}

// finalizeTurn does the last in-place edit (if the buffer fits) or splits
// across multiple messages when it exceeds Telegram's per-message cap.
// emptyFallback is substituted when no content was streamed.
func (sr *StreamRenderer) finalizeTurn(text *strings.Builder, tools []toolEntry, messageID *int, lastBody string, lastParseHTML bool, emptyFallback string) {
	body, isHTML := composeBody(text.String(), tools)
	if body == "" {
		if emptyFallback == "" {
			emptyFallback = "(no response)"
		}
		body = emptyFallback
		isHTML = false
	}

	// HTML body is composed to fit in a single Telegram message when possible;
	// if composeBody returned the plaintext fallback, it may still need
	// chunking across messages.
	var chunks []string
	if isHTML {
		chunks = []string{body}
	} else {
		chunks = chunkMessage(body, telegramMsgLimit)
	}

	// First chunk: edit the existing message (send one if none yet).
	first := chunks[0]
	if *messageID == 0 {
		params := &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: sr.chatID},
			Text:   first,
		}
		if isHTML {
			params.ParseMode = "HTML"
		}
		m, err := sr.bot.SendMessage(sr.ctx, params)
		if err != nil {
			sr.logger.Warn("stream: final send failed", "chat_id", sr.chatID, "err", err)
		} else {
			*messageID = m.MessageID
		}
	} else if first != lastBody || isHTML != lastParseHTML {
		params := &telego.EditMessageTextParams{
			ChatID:    telego.ChatID{ID: sr.chatID},
			MessageID: *messageID,
			Text:      first,
		}
		if isHTML {
			params.ParseMode = "HTML"
		}
		_, err := sr.bot.EditMessageText(sr.ctx, params)
		if err != nil {
			sr.logger.Warn("stream: final edit failed", "chat_id", sr.chatID, "err", err)
		}
	}

	// Overflow: additional messages for the remaining chunks (plaintext only).
	for _, chunk := range chunks[1:] {
		_, err := sr.bot.SendMessage(sr.ctx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: sr.chatID},
			Text:   chunk,
		})
		if err != nil {
			sr.logger.Warn("stream: overflow send failed", "chat_id", sr.chatID, "err", err)
			return
		}
	}
}

// composeBody builds the rendered body. Returns (body, isHTML). When the
// HTML layout (expandable blockquote for tools + assistant text below) fits
// within the per-message limit, returns it with isHTML=true. Otherwise
// falls back to a plaintext layout that can be safely chunked or truncated.
func composeBody(text string, tools []toolEntry) (string, bool) {
	plainText := strings.TrimRight(text, "\n")

	if len(tools) == 0 {
		if plainText == "" {
			return "", false
		}
		// If the text contains markdown code markers, render as HTML so code
		// blocks and inline spans get proper formatting.
		if strings.ContainsRune(plainText, '`') {
			htmlText := mdCodeToHTML(plainText)
			if len(htmlText) <= telegramMsgLimit {
				return htmlText, true
			}
		}
		return plainText, false
	}

	htmlBody := composeHTML(plainText, tools)
	if len(htmlBody) <= telegramMsgLimit {
		return htmlBody, true
	}
	// Fall back to plain layout so chunking/truncation stay safe.
	return composePlain(plainText, tools), false
}

// composeHTML renders the tool log inside an expandable blockquote, with the
// assistant text below. All dynamic content is HTML-escaped.
func composeHTML(text string, tools []toolEntry) string {
	var b strings.Builder
	b.WriteString("<blockquote expandable>")
	writeToolLog(&b, tools, true)
	b.WriteString("</blockquote>")
	if text != "" {
		b.WriteString("\n\n")
		b.WriteString(mdCodeToHTML(text))
	}
	return b.String()
}

// mdCodeToHTML converts markdown code markers to Telegram HTML: triple-backtick
// fenced blocks become <pre>/<pre><code class="language-xxx">, and inline
// backticks become <code>. Everything else is HTML-escaped. An unclosed fence
// at the end of the input is treated as open and closed virtually so the
// emitted HTML stays well-formed during streaming.
func mdCodeToHTML(text string) string {
	var b strings.Builder
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], "```") {
			langStart := i + 3
			langEnd := langStart
			for langEnd < len(text) && text[langEnd] != '\n' {
				langEnd++
			}
			lang := strings.TrimSpace(text[langStart:langEnd])
			contentStart := langEnd
			if contentStart < len(text) && text[contentStart] == '\n' {
				contentStart++
			}
			closeRel := strings.Index(text[contentStart:], "```")
			var content string
			if closeRel < 0 {
				content = text[contentStart:]
				i = len(text)
			} else {
				content = text[contentStart : contentStart+closeRel]
				i = contentStart + closeRel + 3
			}
			content = strings.TrimRight(content, "\n")
			if lang != "" {
				b.WriteString(`<pre><code class="language-`)
				b.WriteString(html.EscapeString(lang))
				b.WriteString(`">`)
				b.WriteString(html.EscapeString(content))
				b.WriteString("</code></pre>")
			} else {
				b.WriteString("<pre>")
				b.WriteString(html.EscapeString(content))
				b.WriteString("</pre>")
			}
			continue
		}
		if text[i] == '`' {
			if rel := strings.IndexByte(text[i+1:], '`'); rel >= 0 {
				nl := strings.IndexByte(text[i+1:], '\n')
				if nl < 0 || rel < nl {
					b.WriteString("<code>")
					b.WriteString(html.EscapeString(text[i+1 : i+1+rel]))
					b.WriteString("</code>")
					i = i + 1 + rel + 1
					continue
				}
			}
		}
		j := i
		for j < len(text) && text[j] != '`' {
			j++
		}
		b.WriteString(html.EscapeString(text[i:j]))
		i = j
	}
	return b.String()
}

// composePlain renders the same content without HTML markup, for chunked or
// oversized messages where we can't rely on parse-mode.
func composePlain(text string, tools []toolEntry) string {
	var b strings.Builder
	writeToolLog(&b, tools, false)
	if text != "" {
		b.WriteString("\n\n")
		b.WriteString(text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeToolLog emits the grouped tool summary. Adjacent same-name runs are
// compressed into a single line; absolute paths are shown relative to the
// common directory prefix (also emitted as a header when it saves space).
// escape controls HTML-escaping of dynamic content.
func writeToolLog(b *strings.Builder, tools []toolEntry, escape bool) {
	prefix := commonDirPrefix(tools)
	emit := func(s string) {
		if escape {
			b.WriteString(html.EscapeString(s))
		} else {
			b.WriteString(s)
		}
	}
	if prefix != "" {
		emit("📁 " + prefix)
		b.WriteByte('\n')
	}
	first := true
	for _, run := range compressRuns(tools) {
		if !first {
			b.WriteByte('\n')
		}
		first = false
		emit(formatRun(run, prefix))
	}
}

type toolRun struct {
	name    string
	entries []toolEntry
}

// compressRuns folds maximal stretches of adjacent entries sharing a tool
// name into a single run. Chronology is preserved between runs.
func compressRuns(tools []toolEntry) []toolRun {
	var runs []toolRun
	for _, t := range tools {
		if n := len(runs); n > 0 && runs[n-1].name == t.name {
			runs[n-1].entries = append(runs[n-1].entries, t)
			continue
		}
		runs = append(runs, toolRun{name: t.name, entries: []toolEntry{t}})
	}
	return runs
}

// formatRun renders a single run as one line. Display choices per tool:
//   - Bash: each invocation keeps its own command, since the command is the
//     interesting part; multi-entry runs show count + first command.
//   - File tools (Read/Edit/Write/etc): if every entry is the same path,
//     show once with a ×N suffix; otherwise list the distinct relative paths.
//   - Tools with no path info (Grep, Glob): show name + count.
func formatRun(r toolRun, prefix string) string {
	emoji := toolEmoji(r.name)
	count := len(r.entries)

	if r.name == "Bash" {
		if count == 1 {
			return emoji + " Bash: " + truncate(r.entries[0].cmd, 120)
		}
		return emoji + " Bash ×" + strconv.Itoa(count) + ": " + truncate(r.entries[0].cmd, 100)
	}

	paths := distinctRelPaths(r.entries, prefix)
	switch {
	case count == 1 && len(paths) == 1:
		return emoji + " " + r.name + " " + paths[0]
	case count == 1:
		return emoji + " " + r.name
	case len(paths) == 0:
		return emoji + " " + r.name + " ×" + strconv.Itoa(count)
	case len(paths) == 1:
		return emoji + " " + r.name + " " + paths[0] + " ×" + strconv.Itoa(count)
	}
	// Multiple distinct paths: show count and up to 5 relative paths.
	listed := paths
	suffix := ""
	if len(listed) > 5 {
		suffix = ", …"
		listed = listed[:5]
	}
	return emoji + " " + r.name + " ×" + strconv.Itoa(count) + ": " + strings.Join(listed, ", ") + suffix
}

func toolEmoji(name string) string {
	switch name {
	case "Edit", "Write", "MultiEdit":
		return "🔧"
	case "Read":
		return "📖"
	case "Bash":
		return "🖥"
	case "Grep":
		return "🔍"
	case "Glob":
		return "🔎"
	default:
		return "•"
	}
}

// distinctRelPaths returns unique paths in first-seen order, each stripped of
// the common prefix when applicable.
func distinctRelPaths(entries []toolEntry, prefix string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, e := range entries {
		if e.path == "" {
			continue
		}
		rel := relPath(e.path, prefix)
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		out = append(out, rel)
	}
	return out
}

func relPath(p, prefix string) string {
	if prefix != "" && strings.HasPrefix(p, prefix) {
		if rel := strings.TrimPrefix(p, prefix); rel != "" {
			return rel
		}
	}
	return p
}

// commonDirPrefix returns the longest directory prefix shared by every
// absolute path in tools. Empty string when there's nothing meaningful to
// extract (fewer than 2 paths, no shared root, or a prefix so short it
// wouldn't save the reader any characters).
func commonDirPrefix(tools []toolEntry) string {
	var paths []string
	for _, t := range tools {
		if strings.HasPrefix(t.path, "/") {
			paths = append(paths, t.path)
		}
	}
	if len(paths) < 2 {
		return ""
	}
	prefix := paths[0]
	for _, p := range paths[1:] {
		prefix = lcp(prefix, p)
		if prefix == "" {
			return ""
		}
	}
	// Trim to the last directory boundary so we never hand back a partial
	// component (e.g. "/a/b/fo" when one path is "/a/b/foo.go" and another
	// is "/a/b/fox.go").
	if i := strings.LastIndexByte(prefix, '/'); i >= 0 {
		prefix = prefix[:i+1]
	} else {
		return ""
	}
	if len(prefix) < 12 { // not worth a header line
		return ""
	}
	return prefix
}

func lcp(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}
