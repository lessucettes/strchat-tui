package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestContextHintsRetainExistingBindings(t *testing.T) {
	for _, group := range []bool{false, true} {
		for _, mode := range []layoutMode{modeFull, modeNarrow} {
			for _, area := range []focusArea{focusInput, focusChats, focusMessages, focusLogs, focusInfo} {
				t.Run(fmt.Sprintf("group=%t/mode=%d/area=%d", group, mode, area), func(t *testing.T) {
					state := hintsState{mode: mode, area: area, group: group, chooseChat: group}
					text := hintsText(state)
					want := []string{"Ctrl+D", "Tab/Shift+Tab", "Cycle Focus", "Alt+...", "Focus"}
					switch area {
					case focusInput:
						want = append(want, "Enter", "Send", "Ctrl+P/N", "History")
					case focusChats:
						want = append(want, "Space", "Select", "Enter", "Activate", "Del", "Delete")
					case focusMessages:
						want = append(want, "`", "Maximize", "↑/↓", "Scroll")
					case focusLogs:
						want = append(want, "`", "Maximize")
					case focusInfo:
						want = append(want, "↑/↓")
						if group {
							want = append(want, "Chat")
						} else {
							want = append(want, "Scroll")
						}
					}
					for _, binding := range want {
						if !strings.Contains(text, binding) {
							t.Errorf("missing existing hint %q in %q", binding, text)
						}
					}
				})
			}
		}
	}
}

func TestTinyGroupHintsRetainCommonBindings(t *testing.T) {
	cases := []struct {
		name  string
		state hintsState
		want  []string
	}{
		{"input", hintsState{mode: modeTiny, area: focusInput, group: true}, []string{"Alt+N", "Info"}},
		{"group info", hintsState{mode: modeTiny, area: focusInfo, group: true, chooseChat: true, infoTemporary: true}, []string{"↑/↓", "Chat", "Alt+I", "Input"}},
		{"relay info", hintsState{mode: modeTiny, area: focusInfo, infoTemporary: true}, []string{"↑/↓", "Scroll", "Alt+I", "Input"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := hintsText(tc.state)
			for _, want := range append([]string{"Ctrl+D", "Tab", "/help"}, tc.want...) {
				if !strings.Contains(text, want) {
					t.Errorf("tiny context lost hint %q: %q", want, text)
				}
			}
			if width := tview.TaggedStringWidth(text); width > 58 {
				t.Errorf("tiny hints occupy %d cells, clipping the restored controls", width)
			}
		})
	}
}

func TestGroupInputHistoryHintAndControls(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			s := newSimTUI(t, width, 32)
			s.onLoop(func() {
				s.ui.applyEvent(focusGroupState())
				s.ui.render()
			})
			s.draw()
			lines := strings.Split(strings.TrimRight(s.text(), "\n"), "\n")
			if hint := lines[len(lines)-1]; !strings.Contains(hint, "Ctrl+P/N: History") {
				t.Fatalf("History hint is not visible in the actual footer: %q", hint)
			}
			s.onLoop(func() {
				s.ui.rememberInput("previous")
				s.ui.input.SetText("draft")
				handler := s.ui.input.InputHandler()
				handler(tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModNone), func(tview.Primitive) {})
				if text := s.ui.input.GetText(); text != "previous" {
					t.Errorf("Ctrl+P did not recall history: %q", text)
				}
				handler(tcell.NewEventKey(tcell.KeyCtrlN, 0, tcell.ModNone), func(tview.Primitive) {})
				if text := s.ui.input.GetText(); text != "draft" {
					t.Errorf("Ctrl+N did not restore the draft: %q", text)
				}
			})
		})
	}
}
