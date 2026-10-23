package tui

// Persistent saturated-scrollback benchmarks. They keep the production shape
// that the previous benchmark missed: an already-saturated bounded history,
// production-sized display metadata, one (or a burst of) appended messages per
// draw, ordinary and group panes, every layout class, and both the line and the
// byte retention cap.
//
// Run with the repository benchmark command, for example:
//
//	go test ./internal/tui -run '^$' -bench '^BenchmarkSaturatedScrollback$' \
//	    -benchmem -benchtime=300ms -count=3

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/lessucettes/strchat-tui/internal/client"
)

// scrollbackBenchLayout names one of the layout size classes.
type scrollbackBenchLayout struct {
	name string
	w, h int
}

var scrollbackBenchLayouts = []scrollbackBenchLayout{
	{"full-120x40", 120, 40},
	{"narrow-80x32", 80, 32},
	{"tiny-58x20", 58, 20},
	{"tiny-16x10", 16, 10},
}

// newSaturatedBenchTUI builds a TUI on a simulation screen and fills its active
// view's scrollback to the requested cap with production-shaped events.
func newSaturatedBenchTUI(b *testing.B, group bool, layout scrollbackBenchLayout, contentBytes, fill int) (*tui, tcell.SimulationScreen) {
	b.Helper()
	ui := New(make(chan client.UserAction, 32), nil)
	screen := tcell.NewSimulationScreen("UTF-8")
	ui.app.SetScreen(screen)
	screen.SetSize(layout.w, layout.h)

	views := []client.View{{Name: "lobby"}}
	nicks := map[string]string{"lobby": "alice"}
	if group {
		views = []client.View{{Name: "group", IsGroup: true, Children: []string{"lobby", "coffee"}, SelectedChat: "lobby"}}
	}
	ui.applyEvent(client.DisplayEvent{Type: "STATE_UPDATE", Payload: client.StateUpdate{
		Views: views, ActiveViewIndex: 0, ChatNicks: nicks,
	}})
	for range fill {
		ui.applyEvent(saturatedBenchMessage(group, contentBytes))
	}
	ui.app.ForceDraw()
	return ui, screen
}

// saturatedBenchMessage is one chat event with the display metadata the real
// client emits: a four-character short key and a full 64-byte public key.
func saturatedBenchMessage(group bool, contentBytes int) client.DisplayEvent {
	chat := "lobby"
	if group {
		chat = "lobby" // still a child of the group view
	}
	content := strings.Repeat("word ", contentBytes/5) + strings.Repeat("x", contentBytes%5)
	return client.DisplayEvent{
		Type:        "NEW_MESSAGE",
		Chat:        chat,
		Nick:        "bob",
		ShortPubKey: "4321",
		FullPubKey:  strings.Repeat("a", 64),
		Content:     content,
		Timestamp:   "12:00:00",
		ID:          "1234",
	}
}

// BenchmarkSaturatedScrollback measures one render pass with a saturated
// bounded scrollback: append `batch` messages and draw once. It covers the line
// cap (128-byte bodies) and the byte cap (2000-byte bodies), ordinary and group
// panes, and all four layout classes.
func BenchmarkSaturatedScrollback(b *testing.B) {
	for _, cap := range []struct {
		name    string
		content int
	}{
		{"line-cap/128B", 128},
		{"byte-cap/2000B", 2000},
	} {
		for _, group := range []bool{false, true} {
			for _, layout := range scrollbackBenchLayouts {
				for _, batch := range []int{1, 100} {
					name := fmt.Sprintf("%s/group=%v/%s/batch=%d", cap.name, group, layout.name, batch)
					b.Run(name, func(b *testing.B) {
						ui, screen := newSaturatedBenchTUI(b, group, layout, cap.content, maxOutputLines)
						defer ui.restoreLog()
						defer screen.Fini()
						ev := saturatedBenchMessage(group, cap.content)

						b.ReportAllocs()
						b.ReportMetric(float64(batch), "messages/op")
						for b.Loop() {
							for range batch {
								ui.applyEvent(ev)
							}
							ui.app.ForceDraw()
						}
						b.ReportMetric(float64(ui.outputView.winRows), "window-rows")
						b.ReportMetric(float64(ui.outputView.winBytes), "window-bytes")
					})
				}
			}
		}
	}
}

// BenchmarkSaturatedLogs measures the bounded log pane with the same append +
// draw shape. Logs share the scrollback path and were previously rewritten in
// full on every eviction.
func BenchmarkSaturatedLogs(b *testing.B) {
	for _, content := range []struct {
		name string
		size int
	}{
		{"short-80B", 80},
		{"long-2000B", 2000},
	} {
		for _, layout := range scrollbackBenchLayouts[:3] {
			b.Run(fmt.Sprintf("%s/%s", content.name, layout.name), func(b *testing.B) {
				ui, screen := newSaturatedBenchTUI(b, false, layout, 128, 1)
				defer ui.restoreLog()
				defer screen.Fini()
				ev := client.DisplayEvent{Type: "STATUS", Content: strings.Repeat("s", content.size)}
				for range maxLogLines + 50 {
					ui.applyEvent(ev)
				}
				ui.app.ForceDraw()

				b.ReportAllocs()
				for b.Loop() {
					ui.applyEvent(ev)
					ui.app.ForceDraw()
				}
			})
		}
	}
}

// BenchmarkScrollbackScroll measures redraws while a saturated history is being
// scrolled instead of followed. A scroll inside the cached window must not
// rebuild the whole retained history.
func BenchmarkScrollbackScroll(b *testing.B) {
	for _, group := range []bool{false, true} {
		b.Run(fmt.Sprintf("group=%v", group), func(b *testing.B) {
			layout := scrollbackBenchLayouts[0]
			ui, screen := newSaturatedBenchTUI(b, group, layout, 128, maxOutputLines)
			defer ui.restoreLog()
			defer screen.Fini()

			b.ReportAllocs()
			up := true
			for b.Loop() {
				row, _ := ui.outputView.GetScrollOffset()
				if up && row > 0 {
					row--
				} else if !up {
					row++
				}
				up = !up
				ui.outputView.ScrollTo(row, 0)
				ui.app.ForceDraw()
			}
		})
	}
}
