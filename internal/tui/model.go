package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// Scrollback budgets. Both a line count and a byte count are enforced so
// retained memory stays bounded even when every line sits at the client's
// verified 16 KiB content limit.
const (
	maxOutputLines = 1000
	maxOutputBytes = 1 << 20 // 1 MiB of rendered chat scrollback
	maxLogLines    = 500
	maxLogBytes    = 256 << 10 // 256 KiB of rendered log/status lines

	// maxLineRunes caps one rendered payload. Verified client content is
	// capped at 16 KiB, so real messages never hit this limit.
	maxLineRunes = 16 * 1024

	// maxPayloadRunes caps client-generated multi-line payloads (help texts,
	// relay and block lists) that have no per-message cap.
	maxPayloadRunes = 64 * 1024
)

// lineBuffer is a bounded FIFO of display lines in canonical default-theme
// markup. The event loop maps colors when writing widgets, so themes can change
// without modifying model content or sharing mutable styles with collectors.
// Lines are addressed by an absolute index that never decreases, which lets a view
// append only the lines it has not written yet.
//
// lineBuffer is not safe for concurrent use: callers hold the owning tui lock.
type lineBuffer struct {
	maxLines int
	maxBytes int
	lines    []string
	bytes    int
	head     int   // index in lines of the oldest retained line
	count    int   // number of retained lines
	first    int64 // absolute index of the oldest retained line
}

func newLineBuffer(maxLines, maxBytes int) *lineBuffer {
	if maxLines < 1 {
		maxLines = 1
	}
	return &lineBuffer{
		maxLines: maxLines,
		maxBytes: maxBytes,
		lines:    make([]string, maxLines),
	}
}

// append adds a line, evicting the oldest lines until both bounds hold. A
// single line larger than maxBytes is still retained (bounded by maxLineRunes).
func (b *lineBuffer) append(line string) {
	for b.count > 0 && (b.count >= b.maxLines || (b.maxBytes > 0 && b.bytes+len(line) > b.maxBytes)) {
		b.dropOldest()
	}
	idx := (b.head + b.count) % b.maxLines
	b.lines[idx] = line
	b.count++
	b.bytes += len(line)
}

func (b *lineBuffer) dropOldest() {
	b.bytes -= len(b.lines[b.head])
	b.lines[b.head] = ""
	b.head = (b.head + 1) % b.maxLines
	b.count--
	b.first++
}

// firstIndex returns the absolute index of the oldest retained line.
func (b *lineBuffer) firstIndex() int64 { return b.first }

// lastIndex returns the absolute index of the newest retained line, or -1.
func (b *lineBuffer) lastIndex() int64 { return b.first + int64(b.count) - 1 }

// snapshot returns all retained lines in display order.
func (b *lineBuffer) snapshot() []string {
	out := make([]string, 0, b.count)
	for i := 0; i < b.count; i++ {
		out = append(out, b.lines[(b.head+i)%b.maxLines])
	}
	return out
}

// unsynced returns the retained lines with absolute index >= from. The ok
// result is false when the caller's cursor fell out of the buffer, meaning the
// view must be rewritten from a full snapshot instead.
func (b *lineBuffer) unsynced(from int64) (lines []string, ok bool) {
	if from < b.first {
		return nil, false
	}
	if b.count == 0 || from > b.lastIndex() {
		return nil, true
	}
	skip := int(from - b.first)
	out := make([]string, 0, b.count-skip)
	for i := skip; i < b.count; i++ {
		out = append(out, b.lines[(b.head+i)%b.maxLines])
	}
	return out, true
}

// sanitizeForDisplay makes untrusted text safe for a dynamic-colors TextView:
// tview markup tags are escaped and control/format runes are removed. Newlines
// survive because payloads are rendered as they arrive (this mirrors the
// pre-hardening layout).
//
// The client verifies and caps payloads, but the UI escapes anyway: display
// code must not depend on upstream validation for correctness.
func sanitizeForDisplay(s string) string {
	return tview.Escape(sanitizeRunes(s))
}

// sanitizeLineForDisplay is sanitizeForDisplay for single-line fields (nicks,
// chat names, keys, timestamps); line breaks become spaces.
func sanitizeLineForDisplay(s string) string {
	return tview.Escape(sanitizeRunes(strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		return r
	}, s)))
}

// sanitizeRunes drops C0/C1 controls, ESC, bidi overrides and other invisible
// formatting runes that could corrupt the terminal layout. Tabs and carriage
// returns become spaces; newlines are kept.
func sanitizeRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\r' || r == '\t' || r == '\u2028' || r == '\u2029':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f: // C0 controls, DEL, ESC
		case r >= 0x80 && r <= 0x9f: // C1 controls
		case r >= 0x200b && r <= 0x200f: // zero-width and bidi marks
		case r >= 0x202a && r <= 0x202e: // bidi embedding/override
		case r >= 0x2060 && r <= 0x2064: // word joiner and invisible operators
		case r >= 0x2066 && r <= 0x2069: // bidi isolates
		case r == 0xfeff || r == 0x00ad: // BOM, soft hyphen
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// truncateRunes bounds a payload to max runes, appending an ellipsis so the
// reader can see that something was dropped. Truncation happens before markup
// escaping so tags are never cut in half.
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// splitLogLines splits one log chunk into display lines, dropping empty ones.
func splitLogLines(s string) []string {
	raw := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimRight(line, " \r\t")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// colorTag renders a color as a tview foreground tag. tcell's Color.String()
// picks a name by iterating a map ("gray"/"grey" differ between calls), so the
// canonical hex form is used instead: rendering stays deterministic.
func colorTag(color tcell.Color) string {
	if !color.Valid() {
		return "[default]"
	}
	return fmt.Sprintf("[#%06x]", color.Hex())
}

// colorBoldTag is colorTag with the bold attribute.
func colorBoldTag(color tcell.Color) string {
	if !color.Valid() {
		return "[default::b]"
	}
	return fmt.Sprintf("[#%06x::b]", color.Hex())
}

// messageStyle carries the render-time choices for one chat message line.
type messageStyle struct {
	label     string // optional "[color]chat[-] " prefix for group traffic
	nickTag   string // markup tag for the sender nick (from pubkeyToColor)
	ownColor  tcell.Color
	metaColor tcell.Color
	mention   string // "@nick" to emphasize, "" for none
}

// formatMessage renders one NEW_MESSAGE event as a single TextView markup
// line. Only our own tags stay active: nick, key, content, id and timestamp
// are escaped, so message text cannot inject colors or control the layout.
// Content is rendered as it arrives, including embedded newlines.
func formatMessage(ev client.DisplayEvent, st messageStyle) string {
	nick := sanitizeLineForDisplay(ev.Nick)
	shortKey := sanitizeLineForDisplay(ev.ShortPubKey)
	content := highlightMention(truncateRunes(ev.Content, maxLineRunes), st.mention, st.ownColor)
	meta := fmt.Sprintf("%s[%s %s][-]", colorTag(st.metaColor), sanitizeLineForDisplay(ev.ID), sanitizeLineForDisplay(ev.Timestamp))
	if ev.IsOwnMessage {
		return fmt.Sprintf("%s%s%s[-::-]#%s> %s%s[-] %s",
			st.label, colorBoldTag(st.ownColor), nick, shortKey, colorTag(st.ownColor), content, meta)
	}
	return fmt.Sprintf("%s%s%s[-::-]#%s> %s %s", st.label, st.nickTag, nick, shortKey, content, meta)
}

// highlightMention emphasizes every occurrence of mention in content while
// escaping everything else, including the mention itself.
func highlightMention(content, mention string, color tcell.Color) string {
	if mention == "" || !strings.Contains(content, mention) {
		return sanitizeForDisplay(content)
	}
	parts := strings.Split(content, mention)
	var b strings.Builder
	b.Grow(len(content) + 16)
	for i, part := range parts {
		if i > 0 {
			b.WriteString(colorBoldTag(color))
			b.WriteString(sanitizeForDisplay(mention))
			b.WriteString("[-::-]")
		}
		b.WriteString(sanitizeForDisplay(part))
	}
	return b.String()
}

// formatInfo renders an INFO payload for the message view.
func formatInfo(content string, color tcell.Color) string {
	content = truncateRunes(strings.TrimSpace(content), maxPayloadRunes)
	return fmt.Sprintf("%s-- %s[-]", colorTag(color), sanitizeForDisplay(content))
}

// formatLogLine renders a STATUS/ERROR event for the log view.
func formatLogLine(level, content string, color tcell.Color, timestamp string) string {
	content = truncateRunes(strings.TrimSpace(content), maxPayloadRunes)
	return fmt.Sprintf("%s[%s] %s: %s[-]", colorTag(color), timestamp, sanitizeLineForDisplay(level), sanitizeForDisplay(content))
}

// formatAutoLogLine renders a line captured from the standard logger.
func formatAutoLogLine(timestamp, message string, color tcell.Color) string {
	message = truncateRunes(strings.TrimSpace(message), maxPayloadRunes)
	return fmt.Sprintf("%s[%s] %s[-]", colorTag(color), timestamp, sanitizeForDisplay(message))
}
