package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/lessucettes/strchat-tui/internal/client"
)

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

func TestLineBufferLinesFrom(t *testing.T) {
	b := newLineBuffer(3, 0)
	b.append("a")
	b.append("b")

	if lines := b.linesFrom(0); strings.Join(lines, ",") != "a,b" {
		t.Fatalf("linesFrom(0) = %q, want a,b", lines)
	}
	if lines := b.linesFrom(2); len(lines) != 0 {
		t.Fatalf("linesFrom(2) = %q, want empty", lines)
	}

	// Evict the first line: the caller cannot recover it from this buffer.
	b.append("c")
	b.append("d")
	if lines := b.linesFrom(0); len(lines) != 0 {
		t.Fatalf("linesFrom(0) = %q after eviction, want empty", lines)
	}
	if lines := b.linesFrom(2); strings.Join(lines, ",") != "c,d" {
		t.Fatalf("linesFrom(2) = %q, want c,d", lines)
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
		{"zero width joiner preserved", "👩\u200d💻", "👩\u200d💻"},
		{"zero width non-joiner preserved", "می\u200cروم", "می\u200cروم"},
		{"isolate", "a\u2066b\u2069", "ab"},
		{"bom", "a\ufeffb", "ab"},
		{"soft hyphen", "a\u00adb", "ab"},
		{"newline kept", "a\nb", "a\nb"},
		{"unicode kept", "héllo ✅", "héllo ✅"},
		{"invalid UTF-8 replaced", "a\xffb", "a\ufffdb"},
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

func TestFormatActionMessage(t *testing.T) {
	for _, own := range []bool{false, true} {
		for _, tc := range []struct {
			content string
			want    string
		}{
			{"/me waves at @a[b] [red]👋", "coffee * e[vil]#abcd waves at @a[b] [red]👋 [id1 12:00:00]"},
			{"/me\twaves\nagain", "coffee * e[vil]#abcd waves\nagain [id1 12:00:00]"},
			{"/me", "coffee e[vil]#abcd> /me [id1 12:00:00]"},
			{"/me   ", "coffee e[vil]#abcd> /me    [id1 12:00:00]"},
			{"/message hello", "coffee e[vil]#abcd> /message hello [id1 12:00:00]"},
			{"hello /me waves", "coffee e[vil]#abcd> hello /me waves [id1 12:00:00]"},
		} {
			ev := client.DisplayEvent{Nick: "e[vil]", ShortPubKey: "abcd", Content: tc.content,
				ID: "id1", Timestamp: "12:00:00", IsOwnMessage: own}
			line := formatMessage(ev, messageStyle{label: "coffee ", nickTag: "[#33ccff]",
				mention: "@a[b]", ownColor: tcell.ColorLime, metaColor: tcell.ColorGray})
			view := tview.NewTextView().SetDynamicColors(true)
			view.SetText(line)
			if got := view.GetText(true); got != tc.want {
				t.Errorf("own=%v content=%q: got %q, want %q", own, tc.content, got, tc.want)
			}
			if strings.Contains(line, "[red]") || strings.Contains(line, "[vil]") || strings.Contains(line, "[b]") {
				t.Errorf("action retained injected markup: %q", line)
			}
		}
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
	if want := colorTag(tcell.ColorGray) + "[15:04:05] INFO: " + tview.Escape(`Diagnostic output: "msg [y]".`) + "[-]"; auto != want {
		t.Fatalf("formatAutoLogLine = %q, want %q", auto, want)
	}
	// A relay's rejection must not forge another entry or inject markup, while
	// identifiers and original reason text keep their case.
	forged := formatLogLine("WARN", "Relay wss://Example.org/Path: denied\n[00:00:00] INFO: [green]accepted", tcell.ColorYellow, "15:04:05")
	if strings.Contains(forged, "\n") || strings.Contains(forged, "[green]") || !strings.Contains(forged, "Example.org/Path") {
		t.Fatalf("log line lost literal context or allowed an injected entry: %q", forged)
	}
}

func TestSplitLogLines(t *testing.T) {
	got := splitLogLines("one\ntwo\r\n\nthree\n")
	want := []string{"one", "two", "three"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("splitLogLines = %v, want %v", got, want)
	}
}
