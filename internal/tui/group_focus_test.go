package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"

	"github.com/lessucettes/strchat-tui/internal/client"
)

func focusGroupState() client.DisplayEvent {
	return client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{
		Views: []client.View{
			{Name: "mixed", IsGroup: true, Children: []string{"coffee", "ucfv0", "lobby"}},
			{Name: "coffee"}, {Name: "ucfv0"}, {Name: "lobby"},
		}, ActiveViewIndex: 0, ChatNicks: map[string]string{"coffee": "me", "ucfv0": "me", "lobby": "me"},
	}}
}

func TestGroupInfoArrowsChooseConversation(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.events <- focusGroupState()
	s.events <- client.DisplayEvent{Type: "PRESENCE_UPDATE", Payload: map[string]int{"coffee": 12, "ucfv0": 3}}
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return len(s.ui.snapshot().views) == 4 })
	})
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(s.ui.detailsView.GetText(true), "[3] ucfv0") })
	})
	s.draw()
	s.key(tcell.KeyRune, 'c', tcell.ModAlt)
	s.key(tcell.KeyCtrlN, 0, tcell.ModCtrl)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.chatList.GetCurrentItem() == 1 }) })
	s.key(tcell.KeyCtrlP, 0, tcell.ModCtrl)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.chatList.GetCurrentItem() == 0 }) })
	s.key(tcell.KeyRune, 'n', tcell.ModAlt)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.detailsView }) })
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(s.ui.detailsView.GetText(true), "▶ coffee") })
	})
	s.key(tcell.KeyDown, 0, tcell.ModNone)
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "ucfv0" })
	})
	s.key(tcell.KeyCtrlP, 0, tcell.ModCtrl)
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "coffee" })
	})
	s.key(tcell.KeyCtrlN, 0, tcell.ModCtrl)
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "ucfv0" })
	})
	s.key(tcell.KeyRune, 'i', tcell.ModAlt)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.input }) })
	s.draw()
	if got := onLoopValue(s, func() string { return s.ui.detailsView.GetText(true) }); !strings.Contains(got, "▶ ucfv0") {
		t.Fatalf("conversation marker disappeared on returning to input: %q", got)
	}
	if got := onLoopValue(s, func() string { return s.ui.detailsView.GetText(true) }); !strings.Contains(got, "[12] coffee") || !strings.Contains(got, "[0] lobby") {
		t.Fatalf("Info lost per-chat counts or the missing-chat zero: %q", got)
	}
	if active := onLoopValue(s, func() int { return s.ui.snapshot().activeIndex }); active != 0 {
		t.Fatal("choosing a conversation activated a different view")
	}
	for len(s.actions) > 0 {
		if action := <-s.actions; action.Type == "ACTIVATE_VIEW" {
			t.Fatal("choosing a conversation must not change subscriptions")
		}
	}
}

func TestGroupInfoTemporaryAccessOnTinyScreens(t *testing.T) {
	for _, size := range [][2]int{{58, 10}, {80, 14}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			s := newSimTUI(t, size[0], size[1])
			s.events <- focusGroupState()
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return len(s.ui.snapshot().views) == 4 })
			})
			s.onLoop(func() { s.ui.input.SetText("draft") })
			s.key(tcell.KeyRune, 'n', tcell.ModAlt)
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return s.ui.visible(s.ui.detailsView) && s.ui.focused == s.ui.detailsView })
			})
			s.key(tcell.KeyDown, 0, tcell.ModNone)
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "ucfv0" })
			})
			s.key(tcell.KeyRune, 'i', tcell.ModAlt)
			waitFor(t, time.Second, func() bool {
				return s.matches(func() bool { return s.ui.visible(s.ui.outputView) && !s.ui.visible(s.ui.detailsView) })
			})
			if draft := onLoopValue(s, func() string { return s.ui.input.GetText() }); draft != "draft" {
				t.Fatalf("temporary Info changed draft: %q", draft)
			}
		})
	}
}

// presenceGroupState builds an active group whose Info list overflows the pane,
// so presence-only updates can be checked against a manual scroll position.
// The room names fit one row with the old "- " prefix but wrap with a count.
func presenceGroupState() client.DisplayEvent {
	children := []string{"coffee", "カフェ-ロビー-とても長い名前"}
	for i := range 13 {
		children = append(children, fmt.Sprintf("room-%02d-short-presence", i))
	}
	children = append(children, "deep-target")
	return client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{
		Views:           []client.View{{Name: "mixed", IsGroup: true, Children: children}, {Name: "coffee"}},
		ActiveViewIndex: 0,
		ChatNicks:       map[string]string{"coffee": "me"},
	}}
}

func presenceCounts(coffee int) map[string]int {
	counts := map[string]int{"coffee": coffee, "deep-target": 3, "カフェ-ロビー-とても長い名前": 333}
	for i := range 13 {
		counts[fmt.Sprintf("room-%02d-short-presence", i)] = 125 + i
	}
	return counts
}

// A presence snapshot names the count in the Messages title only above two
// participants, keeps the Info marker and zero for missing chats, and never
// rewinds a manually scrolled Info pane or the selection, draft and focus.
func TestPresenceUpdatesTitleAndInfoWithoutRewinding(t *testing.T) {
	s := newSimTUI(t, 120, 32)
	s.events <- presenceGroupState()
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return len(s.ui.snapshot().views) == 2 })
	})
	s.key(tcell.KeyRune, 'n', tcell.ModAlt)
	waitFor(t, time.Second, func() bool { return s.matches(func() bool { return s.ui.focused == s.ui.detailsView }) })
	s.draw()

	// The rendered counts are the barrier for each snapshot: title and Info are
	// painted by the same render pass.
	s.events <- client.DisplayEvent{Type: "PRESENCE_UPDATE", Payload: presenceCounts(2)}
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool {
			return strings.Contains(s.ui.detailsView.GetText(true), "[125] room-00-short-presence")
		})
	})
	if got := onLoopValue(s, func() string { return s.ui.outputView.GetTitle() }); got != "coffee (Alt+O)" {
		t.Fatalf("title with two participants = %q", got)
	}

	s.events <- client.DisplayEvent{Type: "PRESENCE_UPDATE", Payload: presenceCounts(3)}
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(s.ui.outputView.GetTitle(), "· 3 users") })
	})
	if got := onLoopValue(s, func() string { return s.ui.outputView.GetTitle() }); got != "coffee · 3 users (Alt+O)" {
		t.Fatalf("title with three participants = %q", got)
	}

	info := onLoopValue(s, func() string { return s.ui.detailsView.GetText(true) })
	if !strings.Contains(info, "▶ coffee") || !strings.Contains(info, "[125] room-00-short-presence") || strings.Contains(info, "- room-00-short-presence") {
		t.Fatalf("Info does not show the marked conversation beside literal per-chat counts: %q", info)
	}
	if !strings.Contains(info, "[333] カフェ-ロビー-とても長い名前") {
		t.Fatalf("Info lost the count or wide name of a Unicode chat: %q", info)
	}

	// Manual reading position, draft and focus survive a presence-only update.
	s.onLoop(func() {
		s.ui.input.SetText("draft text")
		s.ui.detailsView.ScrollTo(6, 0)
	})
	s.draw()
	if row := onLoopValue(s, func() int { row, _ := s.ui.detailsView.GetScrollOffset(); return row }); row != 6 {
		t.Fatalf("Info did not scroll manually: row %d", row)
	}
	s.events <- client.DisplayEvent{Type: "PRESENCE_UPDATE", Payload: presenceCounts(4)}
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(s.ui.outputView.GetTitle(), "· 4 users") })
	})
	s.draw()
	if row := onLoopValue(s, func() int { row, _ := s.ui.detailsView.GetScrollOffset(); return row }); row != 6 {
		t.Fatalf("presence update rewound Info to row %d, want 6", row)
	}
	if s.matches(func() bool { return !s.ui.visible(s.ui.input) || s.ui.focused != s.ui.detailsView }) {
		t.Fatal("presence update changed focus")
	}
	if got := onLoopValue(s, func() string { return s.ui.input.GetText() }); got != "draft text" {
		t.Fatalf("presence update changed the draft: %q", got)
	}
	if got := onLoopValue(s, func() string { return s.ui.conversationChat(s.ui.snapshot()) }); got != "coffee" {
		t.Fatalf("presence update changed the destination: %q", got)
	}
	if index := onLoopValue(s, func() int { return s.ui.chatList.GetCurrentItem() }); index != 0 {
		t.Fatalf("presence update changed the chat-list selection: %d", index)
	}

	// Revealing a chat below the fold must count the rendered count prefix.
	s.onLoop(func() { s.ui.selectConversation(s.ui.snapshot(), "deep-target") })
	waitFor(t, time.Second, func() bool {
		return s.matches(func() bool { return s.ui.conversationChat(s.ui.snapshot()) == "deep-target" })
	})
	s.draw()
	if screen := s.text(); !strings.Contains(screen, "▶ deep-target") {
		t.Fatalf("selected chat was not revealed through wrapped count rows:\n%s", screen)
	}

	// /clear affects neither presence nor Logs. Counts keep updating even
	// before the client acknowledges the message-clearing boundary.
	logsBefore := onLoopValue(s, func() string { return s.ui.logsView.GetText(true) })
	enterText(s, "/clear")
	s.draw()
	if got := onLoopValue(s, func() string { return s.ui.outputView.GetTitle() }); got != "deep-target · 3 users (Alt+O)" {
		t.Fatalf("clear changed the participant title: %q", got)
	}
	info = onLoopValue(s, func() string { return s.ui.detailsView.GetText(true) })
	if !strings.Contains(info, "[125] room-00-short-presence") {
		t.Fatalf("clear changed presence counts: %q", info)
	}
	if logs := onLoopValue(s, func() string { return s.ui.logsView.GetText(true) }); logs != logsBefore {
		t.Fatal("clear changed Logs")
	}

	duringClear := presenceCounts(9)
	duringClear["deep-target"] = 9
	s.events <- client.DisplayEvent{Type: "PRESENCE_UPDATE", Payload: duringClear}
	s.events <- client.DisplayEvent{Type: "STATUS", Content: "presence barrier"}
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(s.ui.logsView.GetText(true), "presence barrier") })
	})
	s.draw()
	if got := onLoopValue(s, func() string { return s.ui.outputView.GetTitle() }); got != "deep-target · 9 users (Alt+O)" {
		t.Fatalf("clear blocked a live presence update: %q", got)
	}

	s.events <- client.DisplayEvent{Type: "CLEARED"}
	s.events <- client.DisplayEvent{Type: "PRESENCE_UPDATE", Payload: presenceCounts(4)}
	waitFor(t, 5*time.Second, func() bool {
		// deep-target is the active conversation and has three participants.
		return s.matches(func() bool { return strings.Contains(s.ui.outputView.GetTitle(), "· 3 users") })
	})

	// Ordinary chats and narrow layouts retain their name, count and shortcut.
	state := presenceGroupState().Payload.(client.StateUpdate)
	state.ActiveViewIndex = 1
	s.events <- client.DisplayEvent{Type: "STATE_UPDATE", Payload: state}
	waitFor(t, 5*time.Second, func() bool {
		return s.matches(func() bool { return strings.Contains(s.ui.outputView.GetTitle(), "· 4 users") })
	})
	s.resize(80, 24)
	s.draw()
	if got := onLoopValue(s, func() string { return s.ui.outputView.GetTitle() }); got != "coffee · 4 users (Alt+O)" {
		t.Fatalf("narrow layout title = %q", got)
	}
	s.resize(58, 10)
	s.draw()
	if got := onLoopValue(s, func() string { return s.ui.outputView.GetTitle() }); got != "coffee · 4 users (Alt+O)" {
		t.Fatalf("tiny layout title = %q", got)
	}
	s.resize(120, 32)
	s.draw()
	if got := onLoopValue(s, func() string { return s.ui.outputView.GetTitle() }); got != "coffee · 4 users (Alt+O)" {
		t.Fatalf("full layout title after resize = %q", got)
	}
}

func TestConversationTitleClipsNamesBeforeMetadata(t *testing.T) {
	for index, name := range []string{
		strings.Repeat("long-room-", 20),
		strings.Repeat("カフェe\u0301👩‍💻", 20),
		"[red]\n" + strings.Repeat("room-", 20),
	} {
		for _, width := range []int{24, 40, 80} {
			t.Run(fmt.Sprintf("name%d/width%d", index, width), func(t *testing.T) {
				ui := newIdleTUI(t, 16)
				ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{
					Views:           []client.View{{Name: "group", IsGroup: true, Children: []string{"other", name}, SelectedChat: name}},
					ActiveViewIndex: 0,
				}})
				ui.applyEvent(client.DisplayEvent{Type: "PRESENCE_UPDATE", Payload: map[string]int{name: 27}})
				ui.outputView.SetRect(0, 0, width, 8)
				ui.render()
				title := ui.outputView.GetTitle()
				const suffix = " · 27 users (Alt+O)"
				if !strings.HasSuffix(title, "…"+suffix) || tview.TaggedStringWidth(title) > width-2 {
					t.Fatalf("long name displaced metadata or overflowed %d cells: %q", width, title)
				}
				prefix := strings.TrimSuffix(title, "…"+suffix)
				// Compare complete grapheme prefixes, escaped only after clipping.
				plain := strings.ReplaceAll(name, "\n", " ")
				boundary := prefix == ""
				g := uniseg.NewGraphemes(plain)
				var kept strings.Builder
				for g.Next() {
					kept.WriteString(g.Str())
					if tview.Escape(kept.String()) == prefix {
						boundary = true
						break
					}
				}
				if !boundary {
					t.Fatalf("title split a grapheme or interpreted chat markup: %q", title)
				}
			})
		}
	}
}
