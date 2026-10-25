package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestConversationThreadKeepsMessagesAndWrappedContinuations(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- focusGroupState()
	for i, item := range []struct{ chat, text string }{
		{"coffee", strings.Repeat("wrapped ", 25)},
		{"lobby", "other conversation stays readable"},
		{"coffee", "first line\nsecond line"},
	} {
		ev := testMessage(i)
		ev.Chat, ev.Content = item.chat, item.text
		s.events <- ev
	}
	waitOutput(t, s, 3)
	s.draw()
	s.key(tcell.KeyRune, 'n', tcell.ModAlt)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.detailsView }) })
	s.key(tcell.KeyHome, 0, tcell.ModNone)
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "coffee" })
	})
	s.draw()
	text := s.text()
	if got := strings.Count(text, "●"); got != 2 {
		t.Fatalf("thread has %d nodes, want one per focused message:\n%s", got, text)
	}
	for _, wanted := range []string{"other conversation stays readable", "first line", "second line"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("thread hid message %q:\n%s", wanted, text)
		}
	}
	x, y := s.findByPrefix("second line")
	if x < 0 {
		t.Fatal("multiline continuation not visible")
	}
	left := onLoopValue(s, func() int { xx, _, _, _ := s.ui.outputView.GetInnerRect(); return xx - 2 })
	cells, width, _ := s.contents()
	if got := cells[y*width+left].Runes; len(got) == 0 || got[0] != '│' {
		t.Fatalf("thread guide missing beside a multiline continuation: %q", string(got))
	}
	// Reading a different conversation changes only nodes, not content or scroll.
	before := onLoopValue(s, func() int { row, _ := s.ui.outputView.GetScrollOffset(); return row })
	s.key(tcell.KeyDown, 0, tcell.ModNone) // ucfv0 has no messages yet.
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "ucfv0" })
	})
	s.draw()
	if strings.Contains(s.text(), "●") {
		t.Fatal("old conversation nodes remained after focus switch")
	}
	after := onLoopValue(s, func() int { row, _ := s.ui.outputView.GetScrollOffset(); return row })
	if after != before {
		t.Fatalf("focus moved scroll position: %d -> %d", before, after)
	}
}
