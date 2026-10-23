package client

// End-to-end relay pipeline benchmarks. Unlike a library receive-only
// benchmark, these drive the real relay worker (dial -> subscribe -> wire read
// -> envelope/event decode -> bounded queue) and fold each event through the
// same processEvent path the owner loop uses.
//
// unique: a bounded pool of distinct events is cycled, clearing each seen ID
// before processing so full validation (including signature verification) runs.
// This is a full-validation workload, not an unbounded distinct-event stream.
// duplicate: the same event is sent repeatedly and is
// stopped by the seen cache, isolating transport+decode+dedup from crypto.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

const pipelinePoolSize = 1024

var (
	pipelineOnce   sync.Once
	pipelineFrames map[string][][]byte
)

// pipelineFixtures signs a bounded pool of fresh events once per benchmark
// process. Every event carries its pool index in a tag, so IDs are unique even
// when the long body and timestamp repeat. The long body is the same valid
// 2000-grapheme message used by the other performance fixtures.
func pipelineFixtures(b *testing.B) map[string][][]byte {
	pipelineOnce.Do(func() {
		longBody := performanceLongContent(b)
		pipelineFrames = make(map[string][][]byte, 2)
		for _, fixture := range []struct {
			name    string
			content string
		}{
			{"short", strings.Repeat("s", 120)},
			{"long", longBody},
		} {
			frames := make([][]byte, pipelinePoolSize)
			for i := range frames {
				event := &nostr.Event{
					Kind: ephChatKind, CreatedAt: nostr.Now(),
					Tags:    nostr.Tags{{"d", "lobby"}, {"n", "peer"}, {"bench", strconv.Itoa(i)}},
					Content: fixture.content,
				}
				if err := event.Sign(performanceTestKey); err != nil {
					b.Fatal(err)
				}
				data, err := json.Marshal([]any{"EVENT", "chat", event})
				if err != nil {
					b.Fatal(err)
				}
				frames[i] = data
			}
			pipelineFrames[fixture.name] = frames
		}
	})
	return pipelineFrames
}

// pipelineClient builds a real client with a disposable config path and a
// drained display channel; it does not run the owner loop so the benchmark can
// account for processing per frame explicitly.
func pipelineClient(b *testing.B, cfg *config) (*client, chan DisplayEvent) {
	b.Helper()
	display := make(chan DisplayEvent, 4096)
	c := newClient(cfg, make(chan UserAction, 256), display)
	b.Cleanup(func() {
		c.Stop()
		done := make(chan struct{})
		go func() { c.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(transportWaitOp):
			b.Errorf("client workers did not exit")
		}
	})
	return c, display
}

// drainPipelineDisplay keeps the bounded display channel moving and is joined
// before the client is stopped.
func drainPipelineDisplay(b *testing.B, display <-chan DisplayEvent) {
	b.Helper()
	stop := make(chan struct{})
	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		for {
			select {
			case <-display:
			case <-stop:
				return
			}
		}
	}()
	b.Cleanup(func() {
		close(stop)
		drain.Wait()
	})
}

// BenchmarkRelayPipeline measures a complete message through the production
// worker and owner-side event processing.
func BenchmarkRelayPipeline(b *testing.B) {
	fixtures := pipelineFixtures(b)
	for _, size := range []string{"short", "long"} {
		frames := fixtures[size]
		b.Run(size, func(b *testing.B) {
			b.Run("unique", func(b *testing.B) { benchmarkRelayPipeline(b, frames, true) })
			b.Run("duplicate", func(b *testing.B) { benchmarkRelayPipeline(b, frames, false) })
		})
	}
}

func benchmarkRelayPipeline(b *testing.B, frames [][]byte, unique bool) {
	ts := newRelayWSServer(b, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		for {
			select {
			case data := <-ts.out:
				ctx, cancel := context.WithTimeout(context.Background(), transportWaitOp)
				err := conn.Write(ctx, websocket.MessageText, data)
				cancel()
				if err != nil {
					return
				}
			case <-time.After(60 * time.Second):
				return
			}
		}
	})
	c, display := pipelineClient(b, relayTestConfig(b, ts.url))
	drainPipelineDisplay(b, display)
	c.updateRelaySubscriptions(map[string][]string{ts.url: {"lobby"}})
	ts.waitREQs(1, transportWaitSlow)

	ack := make(chan struct{}, 1)
	stop := make(chan struct{})
	var processed atomic.Int64
	var owner sync.WaitGroup
	owner.Add(1)
	go func() {
		defer owner.Done()
		for {
			select {
			case ev := <-c.incoming:
				if ev.event == nil {
					continue
				}
				if unique {
					// The pool is reused across iterations; forget the ID so the
					// event exercises the full unique path every time.
					c.seenCache.Remove(ev.event.ID)
				}
				c.processEvent(ev.event, ts.url)
				if !c.seenCache.Contains(ev.event.ID) {
					b.Error("pipeline fixture failed validation instead of exercising the accepted-event path")
				}
				processed.Add(1)
				select {
				case ack <- struct{}{}:
				default:
				}
			case <-stop:
				return
			}
		}
	}()
	b.Cleanup(func() {
		close(stop)
		owner.Wait()
	})

	var index atomic.Uint64
	send := func() {
		frame := frames[0]
		if unique {
			frame = frames[index.Add(1)%uint64(len(frames))]
		}
		select {
		case ts.out <- frame:
		case <-time.After(transportWaitOp):
			b.Fatal("relay did not accept a benchmark frame")
		}
		select {
		case <-ack:
		case <-time.After(transportWaitOp):
			b.Fatal("frame did not reach processEvent")
		}
	}
	send() // warm the connection
	b.SetBytes(int64(len(frames[0])))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		send()
	}
	b.StopTimer()
	if processed.Load() == 0 {
		b.Fatal("no events processed")
	}
}

// BenchmarkRelayWorkerIngest isolates the worker half (wire read, envelope and
// event decode, bounded queue) without owner-side validation or crypto.
func BenchmarkRelayWorkerIngest(b *testing.B) {
	fixtures := pipelineFixtures(b)
	for _, size := range []string{"short", "long"} {
		frames := fixtures[size]
		b.Run(size, func(b *testing.B) {
			benchmarkRelayWorkerIngest(b, frames)
		})
	}
}

func benchmarkRelayWorkerIngest(b *testing.B, frames [][]byte) {
	ts := newRelayWSServer(b, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		for {
			select {
			case data := <-ts.out:
				ctx, cancel := context.WithTimeout(context.Background(), transportWaitOp)
				err := conn.Write(ctx, websocket.MessageText, data)
				cancel()
				if err != nil {
					return
				}
			case <-time.After(60 * time.Second):
				return
			}
		}
	})
	c, display := pipelineClient(b, relayTestConfig(b, ts.url))
	drainPipelineDisplay(b, display)
	c.updateRelaySubscriptions(map[string][]string{ts.url: {"lobby"}})
	ts.waitREQs(1, transportWaitSlow)

	ack := make(chan struct{}, 1)
	stop := make(chan struct{})
	var owner sync.WaitGroup
	owner.Add(1)
	go func() {
		defer owner.Done()
		for {
			select {
			case ev := <-c.incoming:
				if ev.event == nil {
					continue
				}
				select {
				case ack <- struct{}{}:
				default:
				}
			case <-stop:
				return
			}
		}
	}()
	b.Cleanup(func() {
		close(stop)
		owner.Wait()
	})

	var index atomic.Uint64
	send := func() {
		frame := frames[index.Add(1)%uint64(len(frames))]
		select {
		case ts.out <- frame:
		case <-time.After(transportWaitOp):
			b.Fatal("relay did not accept a benchmark frame")
		}
		select {
		case <-ack:
		case <-time.After(transportWaitOp):
			b.Fatal("frame did not reach the worker queue")
		}
	}
	send()
	b.SetBytes(int64(len(frames[0])))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		send()
	}
}
