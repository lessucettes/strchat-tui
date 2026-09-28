package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/lessucettes/strchat-tui/internal/client"
)

func BenchmarkMessageModel(b *testing.B) {
	ui := New(make(chan client.UserAction, 1), nil)
	defer ui.restoreLog()
	ui.applyEvent(testState())
	event := testMessage(1)
	for range maxOutputLines {
		ui.applyEvent(event)
	}
	b.ReportAllocs()
	for b.Loop() {
		ui.applyEvent(event)
	}
}

// Exercise an already-full scrollback, including widget updates and real tcell
// simulation drawing. The bounded tail is rebuilt on eviction even when scrolled
// away from the end; do not benchmark only the empty-buffer fast path.
func BenchmarkSaturatedBatchRender(b *testing.B) {
	ui := New(make(chan client.UserAction, 1), nil)
	defer ui.restoreLog()
	screen := tcell.NewSimulationScreen("UTF-8")
	ui.app.SetScreen(screen)
	defer screen.Fini()
	screen.SetSize(120, 40)
	ui.applyEvent(testState())
	event := testMessage(1)
	for range maxOutputLines {
		ui.applyEvent(event)
	}
	ui.app.ForceDraw()
	b.ReportAllocs()
	for b.Loop() {
		for range 100 {
			ui.applyEvent(event)
		}
		ui.app.ForceDraw()
	}
	b.ReportMetric(100, "messages/op")
}

// This test is also runnable unchanged against the baseline except for the
// output widget's field name. It measures the real channel -> event loop ->
// rendered screen path, rather than a hand-written approximation of batching.
func TestMeasureTUIBurst(t *testing.T) {
	s := newSimTUI(t, 120, 40)
	s.events <- testState()
	const messages = 1000
	marker := "burst-final-marker"
	done := make(chan struct{}, 1)
	draws := 0
	s.onLoop(func() {
		s.ui.app.SetAfterDrawFunc(func(tcell.Screen) {
			draws++
			if strings.Contains(s.ui.outputView.GetText(true), marker) {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		})
	})
	start := time.Now()
	for i := range messages {
		event := testMessage(i)
		if i == messages-1 {
			event.Content = marker
		}
		select {
		case s.events <- event:
		case <-time.After(10 * time.Second):
			t.Fatal("UI stopped consuming burst")
		}
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("burst tail never reached the screen")
	}
	elapsed := time.Since(start)
	count := onLoopValue(s, func() int { return draws })
	t.Logf("%d messages rendered in %v; draws=%d; retained=%d", messages, elapsed, count, s.ui.modelOutputCount())
	if count > 150 {
		t.Fatalf("burst caused %d draws: batching did not hold", count)
	}
}
