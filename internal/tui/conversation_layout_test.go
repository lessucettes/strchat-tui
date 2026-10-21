package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func TestGroupInfoSelectionVisibleWithLongGroupName(t *testing.T) {
	s := newSimTUI(t, 80, 32)
	s.onLoop(func() {
		ev := focusGroupState()
		state := ev.Payload.(client.StateUpdate)
		state.Views[0].Name = strings.Repeat("coffee+ucfv0+lobby", 8)
		ev.Payload = state
		s.ui.applyEvent(ev)
		s.ui.render()
		s.ui.openInfo()
		s.ui.handleInfoKeys(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	})
	s.draw()
	if text := s.text(); !strings.Contains(text, "▶ ucfv0") {
		t.Fatalf("group name displaced the selected member in narrow Info:\n%s", text)
	}
}

func TestThreadWrapIndexMatchesNativeUnicodeRendering(t *testing.T) {
	for _, width := range []int{7, 12, 24, 58, 80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			s := newSimTUI(t, width, 32)
			s.onLoop(func() {
				s.ui.applyEvent(focusGroupState())
				for i, content := range []string{"wide 世界 👩‍💻 [red] literal", "line one\nline two\n", strings.Repeat("word ", 40), "e\u0301 👨‍👩‍👧‍👦"} {
					ev := testMessage(i)
					ev.Chat, ev.Content = "coffee", content
					s.ui.applyEvent(ev)
				}
			})
			s.draw()
			s.onLoop(func() {
				v := s.ui.outputPane
				if len(v.spans) != 4 {
					t.Fatalf("span count = %d, want one per retained message", len(v.spans))
				}
				if got, want := v.spans[len(v.spans)-1].end, s.ui.outputView.GetWrappedLineCount(); got != want {
					t.Errorf("thread/native wrapped row counts differ: %d != %d", got, want)
				}
			})
		})
	}
}
