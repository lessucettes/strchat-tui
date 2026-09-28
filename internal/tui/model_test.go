package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestLineBufferKeepsInsertionOrder(t *testing.T) {
	b := newLineBuffer(4, 0)
	for _, s := range []string{"a", "b", "c"} {
		b.append(s)
	}
	if got, want := strings.Join(b.snapshot(), ","), "a,b,c"; got != want {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}
	if b.firstIndex() != 0 || b.lastIndex() != 2 {
		t.Fatalf("indices = [%d,%d], want [0,2]", b.firstIndex(), b.lastIndex())
	}
}

func TestLineBufferEvictsOldestLines(t *testing.T) {
	b := newLineBuffer(3, 0)
	for _, s := range []string{"1", "2", "3", "4", "5"} {
		b.append(s)
	}
	if got, want := strings.Join(b.snapshot(), ","), "3,4,5"; got != want {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}
	if b.firstIndex() != 2 || b.lastIndex() != 4 {
		t.Fatalf("indices = [%d,%d], want [2,4]", b.firstIndex(), b.lastIndex())
	}
}

func TestLineBufferEvictsByBytes(t *testing.T) {
	b := newLineBuffer(100, 10)
	b.append("1234567") // 7 bytes
	b.append("8901234") // would exceed 10 bytes: the first line must go
	if got, want := strings.Join(b.snapshot(), ","), "8901234"; got != want {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}
	if b.bytes > 10 {
		t.Fatalf("retained bytes = %d, want <= 10", b.bytes)
	}
}

func TestLineBufferSingleOversizedLineStillAppends(t *testing.T) {
	b := newLineBuffer(100, 4)
	b.append("abcdefghij")
	if got := len(b.snapshot()); got != 1 {
		t.Fatalf("retained lines = %d, want 1", got)
	}
}

func TestLineBufferUnsynced(t *testing.T) {
	b := newLineBuffer(3, 0)
	b.append("a")
	b.append("b")

	lines, ok := b.unsynced(0)
	if !ok || strings.Join(lines, ",") != "a,b" {
		t.Fatalf("unsynced(0) = %q ok=%v, want a,b true", lines, ok)
	}
	lines, ok = b.unsynced(2)
	if !ok || len(lines) != 0 {
		t.Fatalf("unsynced(2) = %q ok=%v, want empty true", lines, ok)
	}

	// Evict the first line and ask for it again: the caller must rewrite.
	b.append("c")
	b.append("d")
	if _, ok := b.unsynced(0); ok {
		t.Fatalf("unsynced(0) reported ok after eviction; want rewrite signal")
	}
	lines, ok = b.unsynced(2)
	if !ok || strings.Join(lines, ",") != "c,d" {
		t.Fatalf("unsynced(2) = %q ok=%v, want c,d true", lines, ok)
	}
}

func TestSanitizeForDisplayEscapesMarkup(t *testing.T) {
	in := "[red]styled[-] [::b] [[region]] [#ff0000]"
	got := sanitizeForDisplay(in)
	// Every tag-like sequence must have been escaped: unescaping is exactly
	// the inverse of what the renderer does for escaped text.
	if tview.Unescape(got) != in {
		t.Fatalf("sanitizeForDisplay(%q) = %q is not a lossless escape", in, got)
	}
	if !strings.Contains(got, "[red[]") {
		t.Fatalf("sanitizeForDisplay(%q) = %q did not escape a color tag", in, got)
	}
}

func TestSanitizeForDisplayStripsControlAndFormatRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"nul", "a\x00b", "ab"},
		{"esc sequence", "a\x1b[31mred", "a[31mred"},
		{"carriage return", "a\rb", "a b"},
		{"tab", "a\tb", "a b"},
		{"del", "a\x7fb", "ab"},
		{"c1", "a\u0085b", "ab"},
		{"bidi override", "pay\u202epal", "paypal"},
		{"zero width", "a\u200bb", "ab"},
		{"isolate", "a\u2066b\u2069", "ab"},
		{"bom", "a\ufeffb", "ab"},
		{"soft hyphen", "a\u00adb", "ab"},
		{"newline kept", "a\nb", "a\nb"},
		{"unicode kept", "héllo ✅", "héllo ✅"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeForDisplay(tc.in); got != tc.want {
				t.Fatalf("sanitizeForDisplay(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeLineForDisplayClearsNewlines(t *testing.T) {
	if got := sanitizeLineForDisplay("a\nb\r\nc"); got != "a b  c" {
		t.Fatalf("sanitizeLineForDisplay = %q, want %q", got, "a b  c")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("abcdef", 4); got != "abcd…" {
		t.Fatalf("truncateRunes = %q", got)
	}
	if got := truncateRunes("abc", 4); got != "abc" {
		t.Fatalf("truncateRunes = %q", got)
	}
	if got := truncateRunes("héllo", 5); got != "héllo" {
		t.Fatalf("truncateRunes = %q", got)
	}
}

func TestFormatMessageEscapesUntrustedFields(t *testing.T) {
	ev := client.DisplayEvent{
		Type:        "NEW_MESSAGE",
		Chat:        "chat",
		Nick:        "e[vil]",
		ShortPubKey: "abcd",
		FullPubKey:  "abcdef",
		Content:     "hello [red]world[-]",
		Timestamp:   "12:00:00",
		ID:          "id1",
	}
	got := formatMessage(ev, messageStyle{nickTag: "[#33ccff]", metaColor: tcell.ColorGray})
	if strings.Contains(got, "[red]") {
		t.Fatalf("message line kept an injected tag: %q", got)
	}
	if !strings.Contains(got, tview.Escape("hello [red]world[-]")) {
		t.Fatalf("message line did not escape content: %q", got)
	}
	if !strings.Contains(got, tview.Escape("e[vil]")) {
		t.Fatalf("message line did not escape nick: %q", got)
	}
	if !strings.Contains(got, "#abcd> ") || !strings.Contains(got, "id1 12:00:00") {
		t.Fatalf("message line lost its metadata: %q", got)
	}
}

func TestFormatMessageOwnVariant(t *testing.T) {
	ev := client.DisplayEvent{Nick: "me", Content: "hi", ShortPubKey: "ffff", ID: "id2", Timestamp: "12:00:01", IsOwnMessage: true}
	got := formatMessage(ev, messageStyle{ownColor: tcell.ColorLime, metaColor: tcell.ColorGray})
	for _, want := range []string{colorBoldTag(tcell.ColorLime) + "me[-::-]#ffff> " + colorTag(tcell.ColorLime) + "hi[-]", colorTag(tcell.ColorGray) + "[id2 12:00:01][-]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("own message line %q missing %q", got, want)
		}
	}
}

func TestFormatMessageHighlightsMention(t *testing.T) {
	ev := client.DisplayEvent{Nick: "me", Content: "hey @bob#1234 look", ShortPubKey: "ffff", ID: "id3", Timestamp: "12:00:02"}
	got := formatMessage(ev, messageStyle{mention: "@bob#1234", ownColor: tcell.ColorLime, metaColor: tcell.ColorGray})
	if !strings.Contains(got, colorBoldTag(tcell.ColorLime)+"@bob#1234[-::-]") {
		t.Fatalf("mention was not highlighted: %q", got)
	}
	if !strings.Contains(got, "hey ") || !strings.Contains(got, " look") {
		t.Fatalf("mention highlight lost surrounding text: %q", got)
	}
}

func TestFormatMessageMentionWithMarkupNickCannotInject(t *testing.T) {
	ev := client.DisplayEvent{Nick: "me", Content: "hi @a[b] there", ShortPubKey: "ffff", ID: "id4", Timestamp: "12:00:03"}
	got := formatMessage(ev, messageStyle{mention: "@a[b]", ownColor: tcell.ColorLime, metaColor: tcell.ColorGray})
	if strings.Contains(got, "[b]") {
		t.Fatalf("mention highlight injected an active tag: %q", got)
	}
	if !strings.Contains(got, tview.Escape("@a[b]")) {
		t.Fatalf("mention highlight did not escape the mention: %q", got)
	}
}

func TestFormatInfoAndLogLines(t *testing.T) {
	info := formatInfo("line [one]\nline two", tcell.ColorLime)
	if want := colorTag(tcell.ColorLime) + "-- " + tview.Escape("line [one]\nline two") + "[-]"; info != want {
		t.Fatalf("formatInfo = %q, want %q", info, want)
	}
	logLine := formatLogLine("ERROR", "boom [x]", tcell.ColorRed, "15:04:05")
	if want := colorTag(tcell.ColorRed) + "[15:04:05] ERROR: " + tview.Escape("boom [x]") + "[-]"; logLine != want {
		t.Fatalf("formatLogLine = %q, want %q", logLine, want)
	}
	auto := formatAutoLogLine("15:04:05", "msg [y]", tcell.ColorGray)
	if want := colorTag(tcell.ColorGray) + "[15:04:05] " + tview.Escape("msg [y]") + "[-]"; auto != want {
		t.Fatalf("formatAutoLogLine = %q, want %q", auto, want)
	}
}

func TestColorTagIsDeterministic(t *testing.T) {
	for i := 0; i < 50; i++ {
		if got := colorTag(tcell.ColorGray); got != "[#808080]" {
			t.Fatalf("colorTag(ColorGray) = %q, want [#808080]", got)
		}
	}
	if got := colorBoldTag(tcell.ColorLime); got != "[#00ff00::b]" {
		t.Fatalf("colorBoldTag(ColorLime) = %q, want [#00ff00::b]", got)
	}
	if got := colorTag(tcell.ColorDefault); got != "[default]" {
		t.Fatalf("colorTag(ColorDefault) = %q", got)
	}
}

func TestSplitLogLines(t *testing.T) {
	got := splitLogLines("one\ntwo\r\n\nthree\n")
	want := []string{"one", "two", "three"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("splitLogLines = %v, want %v", got, want)
	}
}

func TestNewLineBufferBounds(t *testing.T) {
	if b := newLineBuffer(0, 0); b.maxLines != 1 {
		t.Fatalf("newLineBuffer(0) maxLines = %d, want 1", b.maxLines)
	}
}
