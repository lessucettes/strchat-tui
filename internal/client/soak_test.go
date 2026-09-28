package client

// Opt-in sustained soak and the always-on repeat-lifecycle leak check.
//
// The soak is gated behind STRCHAT_SOAK_DURATION so `go test ./...` stays fast;
// run it explicitly for sustained verification, for example:
//
//	STRCHAT_SOAK_DURATION=2m  go test -race ./internal/client/ -run TestSoakRelayTransportSteadyState -timeout 10m -v
//	STRCHAT_SOAK_DURATION=30m go test -race ./internal/client/ -run TestSoakRelayTransportSteadyState -timeout 40m -v
//
// Everything runs against loopback httptest websocket relays: real sockets,
// real dial failures, real reconnects. Relay traffic is signed freshly on every
// push (and re-signed on every publish by the client), so events always fall
// inside the client's now-300s..now+60s freshness window no matter how long the
// soak runs; no pre-frozen corpus is replayed.
//
// Measured and asserted: publish/receive success, relay churn, bounded
// goroutines, bounded open file descriptors (Linux /proc/self/fd), bounded
// caches and bounded heap growth, all with explicit tolerances.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
)

const (
	// soakEnvVar gates the sustained soak. Unset or invalid values skip.
	soakEnvVar = "STRCHAT_SOAK_DURATION"
	// soakMaxDuration bounds a single soak even when the env asks for more.
	soakMaxDuration = 30 * time.Minute
)

// soakDuration returns the requested soak duration, or skips the test.
func soakDuration(t *testing.T) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(soakEnvVar))
	if raw == "" {
		t.Skipf("opt-in soak disabled: set %s (for example 2m or 30m) to run it", soakEnvVar)
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		t.Skipf("%s=%q is not a positive duration (for example 2m); skipping", soakEnvVar, raw)
	}
	if d > soakMaxDuration {
		t.Logf("clamping %s=%v to the %v cap", soakEnvVar, d, soakMaxDuration)
		d = soakMaxDuration
	}
	return d
}

// fdCount reports the number of open file descriptors using /proc, or -1 where
// that is unavailable (non-Linux platforms).
func fdCount() int {
	if runtime.GOOS != "linux" {
		return -1
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// soakFreshEvent signs a brand-new event for every push, so long runs never
// replay a stale corpus past the client's freshness window.
func soakFreshEvent(ts *relayTestServer, key string, seq int) *nostr.Event {
	return freshChatEvent(ts, key, "lobby", seq)
}

func soakSendAction(actions chan<- UserAction, a UserAction, d time.Duration) bool {
	select {
	case actions <- a:
		return true
	case <-time.After(d):
		return false
	}
}

// soakSample is one steady-state observation of the running client.
type soakSample struct {
	at          time.Duration
	goroutines  int
	fds         int
	heapAlloc   uint64
	heapObjects uint64
	seen        int
	users       int
}

func soakMaxSamples(samples []soakSample, pick func(soakSample) int) int {
	vals := make([]int, 0, len(samples))
	for _, s := range samples {
		if v := pick(s); v >= 0 {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return -1
	}
	return slices.Max(vals)
}

func soakMinSamples(samples []soakSample, pick func(soakSample) int) int {
	vals := make([]int, 0, len(samples))
	for _, s := range samples {
		if v := pick(s); v >= 0 {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return -1
	}
	return slices.Min(vals)
}

func TestSoakRelayTransportSteadyState(t *testing.T) {
	duration := soakDuration(t)
	t.Logf("soak running for %v (cap %v); set %s to change", duration, soakMaxDuration, soakEnvVar)

	const (
		pushInterval    = 50 * time.Millisecond
		publishInterval = 200 * time.Millisecond
		sampleInterval  = 2 * time.Second
	)
	// Signing identities rotate so the client's user-context cache is
	// exercised by more than one author.
	signingKeys := []string{nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey()}

	var malformedSent atomic.Int64

	// stable: accepts, subscribes, acks every publish with OK true, and pushes
	// freshly signed events (plus the occasional malformed frame) forever.
	stable := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			ticker := time.NewTicker(pushInterval)
			defer ticker.Stop()
			for seq := 1; ; seq++ {
				select {
				case <-stop:
					return
				case <-ticker.C:
					ev := soakFreshEvent(ts, signingKeys[seq%len(signingKeys)], seq)
					if ev == nil {
						return
					}
					if !relayWriteJSON(ts, conn, relayEventFrame("chat", ev)) {
						return
					}
					if seq%40 == 0 {
						malformedSent.Add(1)
						if !relayWriteRaw(ts, conn, mustMarshal([]any{"EVENT", "chat", 42})) {
							return
						}
						if !relayWriteRaw(ts, conn, []byte("soak malformed frame")) {
							return
						}
					}
				}
			}
		}()
		for {
			frame, ok := relayNextFrame(ts, conn)
			if !ok {
				return
			}
			var label string
			if len(frame) < 2 || json.Unmarshal(frame[0], &label) != nil {
				continue
			}
			switch label {
			case "REQ":
				// Subscription replacements after view hops must be recorded.
				relayTestNoteREQ(ts, frame)
			case "EVENT":
				var ev nostr.Event
				if json.Unmarshal(frame[1], &ev) != nil {
					continue
				}
				ts.noteEvent(ev)
				if !relayWriteJSON(ts, conn, relayOKFrame(ev.ID, true, "")) {
					return
				}
			}
		}
	})

	// flappy: subscribes, then drops the socket after tens of milliseconds; it
	// rejects every publish it manages to receive with OK false.
	flappy := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		dropAt := time.Now().Add(25*time.Millisecond + time.Duration(15*(idx%3))*time.Millisecond)
		for time.Now().Before(dropAt) {
			frame, ok := relayNextFrame(ts, conn)
			if !ok {
				return
			}
			var label string
			if len(frame) < 2 || json.Unmarshal(frame[0], &label) != nil || label != "EVENT" {
				continue
			}
			var ev nostr.Event
			if json.Unmarshal(frame[1], &ev) != nil {
				continue
			}
			ts.noteEvent(ev)
			if !relayWriteJSON(ts, conn, relayOKFrame(ev.ID, false, "soak: transient rejection")) {
				return
			}
		}
		conn.CloseNow()
	})

	// strict: always connected and rejects every publish it receives with OK
	// false, so the partial-delivery path (stable accepts, strict rejects) runs
	// deterministically under load. Its received-EVENT count is also its
	// rejection count: every event it gets is answered with OK false.
	strict := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		relayServeAckFrames(ts, conn, false, "soak: policy rejection")
	})

	// dead: refuses the websocket upgrade entirely, so dial retries run for the
	// whole soak.
	var deadDials atomic.Int64
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadDials.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(dead.Close)
	deadURL := "ws" + strings.TrimPrefix(dead.URL, "http")

	type soakCounts struct {
		messages, okPublishes, failedPublishes, rejectsReported, errors, relayUpdates, stateUpdates atomic.Int64
	}
	counts := &soakCounts{}
	var stableConnected atomic.Bool

	events := make(chan DisplayEvent, 4096)
	actions := make(chan UserAction, 64)
	c := newClient(relayTestConfig(t, stable.url, flappy.url, deadURL, strict.url), actions, events)
	clientDone := make(chan struct{})
	go func() { defer close(clientDone); c.Run() }()
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for ev := range events {
			switch ev.Type {
			case "NEW_MESSAGE":
				counts.messages.Add(1)
			case "STATUS":
				if strings.Contains(ev.Content, "acknowledged by ") {
					counts.okPublishes.Add(1)
				}
				if strings.Contains(ev.Content, "relay rejected event") {
					counts.rejectsReported.Add(1)
				}
			case "ERROR":
				counts.errors.Add(1)
				if strings.Contains(ev.Content, "acknowledged by 0/") {
					counts.failedPublishes.Add(1)
				}
			case "RELAYS_UPDATE":
				counts.relayUpdates.Add(1)
				if info, ok := relayInfoFor(ev, stable.url); ok && info.Connected {
					stableConnected.Store(true)
				}
			case "STATE_UPDATE":
				counts.stateUpdates.Add(1)
			}
		}
	}()

	start := time.Now()
	type soakSampleLocal = soakSample
	samples := []soakSampleLocal{}
	samplerStop := make(chan struct{})
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		ticker := time.NewTicker(sampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-samplerStop:
				// Final measurement: after teardown and a forced collection.
				runtime.GC()
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				samples = append(samples, soakSample{at: time.Since(start), goroutines: runtime.NumGoroutine(), fds: fdCount(), heapAlloc: ms.HeapAlloc, heapObjects: ms.HeapObjects, seen: c.seenCache.Len(), users: c.userContext.Len()})
				return
			case <-ticker.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				samples = append(samples, soakSample{at: time.Since(start), goroutines: runtime.NumGoroutine(), fds: fdCount(), heapAlloc: ms.HeapAlloc, heapObjects: ms.HeapObjects, seen: c.seenCache.Len(), users: c.userContext.Len()})
			}
		}
	}()

	loopStop := make(chan struct{})
	var loops sync.WaitGroup
	var loadsOnce, clientOnce sync.Once
	stopLoads := func() {
		loadsOnce.Do(func() {
			close(loopStop)
			loops.Wait()
		})
	}
	stopClient := func() {
		clientOnce.Do(func() {
			c.Stop()
			select {
			case <-clientDone:
			case <-time.After(transportWaitOp):
				t.Errorf("soak: client Run did not return after Stop")
			}
			close(events)
			<-consumerDone
			close(samplerStop)
			<-samplerDone
		})
	}
	t.Cleanup(func() {
		stopLoads()
		stopClient()
	})

	if !waitUntil(t, transportWaitSlow, stableConnected.Load) {
		t.Fatalf("stable relay never reported connected; errors so far: %d", counts.errors.Load())
	}

	// Publisher: steady unique traffic through the real publish path.
	var attempts atomic.Int64
	loops.Add(1)
	go func() {
		defer loops.Done()
		ticker := time.NewTicker(publishInterval)
		defer ticker.Stop()
		for {
			select {
			case <-loopStop:
				return
			case <-ticker.C:
				n := attempts.Add(1)
				if !soakSendAction(actions, UserAction{Type: "SEND_MESSAGE", Payload: fmt.Sprintf("soak publish %d", n)}, 2*time.Second) {
					t.Errorf("soak: publish action could not be queued")
					return
				}
			}
		}
	}()

	// Churn: subscription replacement on the live connection (view hop and
	// back) plus removal/re-add of the flappy worker, which resets its backoff
	// so real reconnect failures keep happening for the whole run.
	//
	// anchors mirrors the client's configured anchor list in order, so the
	// removal below always addresses the flappy relay - after the first
	// re-add the list order is no longer the initial one.
	anchors := []string{stable.url, flappy.url, deadURL, strict.url}
	loops.Add(1)
	go func() {
		defer loops.Done()
		interval := duration / 6
		if interval < 10*time.Second {
			interval = 10 * time.Second
		}
		if interval > 60*time.Second {
			interval = 60 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		cycles := 0
		for {
			select {
			case <-loopStop:
				return
			case <-ticker.C:
				cycles++
				if !soakSendAction(actions, UserAction{Type: "JOIN_CHATS", Payload: "soak-alt"}, 2*time.Second) {
					return
				}
				time.Sleep(300 * time.Millisecond)
				if !soakSendAction(actions, UserAction{Type: "ACTIVATE_VIEW", Payload: "lobby"}, 2*time.Second) {
					return
				}
				// Remove the flappy anchor by its current index (tracked in
				// anchors), then add it back so a fresh worker with fresh
				// backoff starts dialing and failing again.
				if i := slices.Index(anchors, flappy.url); i >= 0 {
					if !soakSendAction(actions, UserAction{Type: "MANAGE_ANCHORS", Payload: fmt.Sprintf("%d", i+1)}, 2*time.Second) {
						return
					}
					anchors = append(anchors[:i], anchors[i+1:]...)
				}
				if !soakSendAction(actions, UserAction{Type: "MANAGE_ANCHORS", Payload: flappy.url}, 2*time.Second) {
					return
				}
				anchors = append(anchors, flappy.url)
				t.Logf("soak churn cycle %d at %v", cycles, time.Since(start).Round(time.Second))
			}
		}
	}()

	// Progress logging while the soak runs.
	logEvery := duration / 10
	if logEvery < 5*time.Second {
		logEvery = 5 * time.Second
	}
	logTicker := time.NewTicker(logEvery)
	defer logTicker.Stop()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		select {
		case <-logTicker.C:
			t.Logf("soak %v/%v: attempts=%d accepted=%d rejected=%d messages=%d errors=%d goroutines=%d fds=%d seen=%d users=%d",
				time.Since(start).Round(time.Second), duration,
				attempts.Load(), counts.okPublishes.Load(), counts.failedPublishes.Load(), counts.messages.Load(),
				counts.errors.Load(), runtime.NumGoroutine(), fdCount(), c.seenCache.Len(), c.userContext.Len())
		case <-time.After(min(time.Until(deadline), 2*time.Second)):
		}
	}

	// Stop the load generators, then prove the client is still healthy with one
	// final publish and one final inbound message.
	stopLoads()
	probeOKBefore := counts.okPublishes.Load()
	probeMessagesBefore := counts.messages.Load()
	if !soakSendAction(actions, UserAction{Type: "SEND_MESSAGE", Payload: "soak final probe"}, 2*time.Second) {
		t.Errorf("soak: final probe publish could not be queued")
	}
	probeOK := waitUntil(t, 5*time.Second, func() bool { return counts.okPublishes.Load() > probeOKBefore })
	probeMessages := waitUntil(t, 5*time.Second, func() bool { return counts.messages.Load() > probeMessagesBefore })

	stopClient()

	// ---- assertions ------------------------------------------------------

	// Successful traffic on real sockets.
	if got := attempts.Load(); got < int64(duration/time.Second)/2 {
		t.Errorf("soak exercised only %d publishes in %v", got, duration)
	}
	ok := counts.okPublishes.Load()
	if ok < attempts.Load()/2 {
		t.Errorf("only %d of %d publishes were acknowledged", ok, attempts.Load())
	}
	if got := counts.messages.Load(); got < int64(duration/time.Second)/2 {
		t.Errorf("only %d inbound messages were displayed in %v", got, duration)
	}
	if !probeOK || !probeMessages {
		t.Errorf("final health probe failed: publish acknowledged=%v, message displayed=%v", probeOK, probeMessages)
	}
	if got := counts.failedPublishes.Load(); got > 0 {
		t.Logf("publishes rejected by every relay (ERROR 0/N): %d", got)
	}

	// Partial delivery under load: the strict relay is always connected and
	// rejects every publish, so at least one rejection must be observed once a
	// publish cycle has run; the client must still report the stable relay's
	// acknowledgement instead of a total failure.
	if duration >= 20*time.Second {
		if got := strict.events.Load(); got == 0 {
			t.Errorf("the always-rejecting relay received no EVENT frames although it is always connected")
		}
		if got := counts.rejectsReported.Load(); got == 0 {
			t.Errorf("no partially rejected publish was reported to the display")
		}
	}

	// Connection lifecycle under load.
	if got := stable.conns.Load(); got != 1 {
		t.Errorf("stable relay saw %d connections; subscription replacement must reuse the single connection", got)
	}
	if got := strict.conns.Load(); got != 1 {
		t.Errorf("strict relay saw %d connections; it must stay connected", got)
	}
	minReqs := int32(1)
	if duration >= 20*time.Second {
		minReqs = 3
	}
	if got := stable.reqs.Load(); got < minReqs {
		t.Errorf("stable relay saw %d REQ frames, want >= %d (subscription replacement under load)", got, minReqs)
	}
	if duration >= 20*time.Second {
		if got := flappy.conns.Load(); got < 2 {
			t.Errorf("flappy relay saw %d connections; reconnect churn did not happen", got)
		}
		if got := deadDials.Load(); got < 2 {
			t.Errorf("rejecting relay saw %d dials; dial retry did not happen", got)
		}
	}
	if malformedSent.Load() == 0 {
		t.Errorf("the soak never injected malformed traffic")
	}

	// Caches stay bounded by their configured sizes.
	seenMax := soakMaxSamples(samples, func(s soakSample) int { return s.seen })
	usersMax := soakMaxSamples(samples, func(s soakSample) int { return s.users })
	if seenMax > seenCacheSize {
		t.Errorf("seen cache grew to %d, cap is %d", seenMax, seenCacheSize)
	}
	if usersMax > userContextCacheSize {
		t.Errorf("user-context cache grew to %d, cap is %d", usersMax, userContextCacheSize)
	}
	if seenMax < 1 || usersMax < 1 {
		t.Errorf("caches were never exercised: seen=%d users=%d", seenMax, usersMax)
	}

	// Steady state: goroutines, descriptors and heap must oscillate inside fixed
	// tolerances rather than grow with time. The final sample is taken after
	// teardown (client stopped, collection forced) and is excluded from the
	// oscillation window, which would otherwise see a spurious low minimum; it
	// is used for the final heap check instead.
	steadyEnd := len(samples) - 1
	if steadyEnd < 2 {
		steadyEnd = len(samples)
	}
	warmup := samples[:max(1, len(samples)/4)]
	steady := samples[len(samples)/4 : steadyEnd]
	if len(steady) > 0 {
		const goroutineSlack = 16
		goroutinesEarly := soakMaxSamples(warmup, func(s soakSample) int { return s.goroutines })
		goroutinesLate := soakMaxSamples(steady, func(s soakSample) int { return s.goroutines })
		goroutinesLateMin := soakMinSamples(steady, func(s soakSample) int { return s.goroutines })
		if goroutinesLate > goroutinesEarly+goroutineSlack {
			t.Errorf("goroutines grew from <=%d to %d (slack %d) across the soak", goroutinesEarly, goroutinesLate, goroutineSlack)
		}
		if goroutinesLate-goroutinesLateMin > goroutineSlack {
			t.Errorf("goroutines oscillated by %d (slack %d) in steady state", goroutinesLate-goroutinesLateMin, goroutineSlack)
		}

		const fdSlack = 12
		if fdsEarly := soakMaxSamples(warmup, func(s soakSample) int { return s.fds }); fdsEarly >= 0 {
			fdsLate := soakMaxSamples(steady, func(s soakSample) int { return s.fds })
			fdsLateMin := soakMinSamples(steady, func(s soakSample) int { return s.fds })
			if fdsLate > fdsEarly+fdSlack {
				t.Errorf("open fds grew from <=%d to %d (slack %d) across the soak", fdsEarly, fdsLate, fdSlack)
			}
			if fdsLate-fdsLateMin > fdSlack {
				t.Errorf("open fds oscillated by %d (slack %d) in steady state", fdsLate-fdsLateMin, fdSlack)
			}
		}

		const heapSlack = 48 << 20
		heapEarly := uint64(0)
		for _, s := range warmup {
			heapEarly = max(heapEarly, s.heapAlloc)
		}
		heapLate := uint64(0)
		for _, s := range steady {
			heapLate = max(heapLate, s.heapAlloc)
		}
		if heapLate > heapEarly+heapSlack {
			t.Errorf("heap grew from %d MiB to %d MiB (slack %d MiB) across the soak", heapEarly>>20, heapLate>>20, heapSlack>>20)
		}
	}
	const finalHeapSlack = 32 << 20
	first := samples[0]
	last := samples[len(samples)-1]
	if last.heapAlloc > first.heapAlloc+finalHeapSlack {
		t.Errorf("heap after teardown is %d MiB, baseline %d MiB (slack %d MiB)", last.heapAlloc>>20, first.heapAlloc>>20, finalHeapSlack>>20)
	}

	t.Logf("soak summary over %v: attempts=%d accepted=%d displayed=%d reports-rejected=%d errors=%d strict-rejected=%d dead-dials=%d flappy-conns=%d malformed-sent=%d",
		duration, attempts.Load(), ok, counts.messages.Load(), counts.rejectsReported.Load(), counts.errors.Load(), strict.events.Load(), deadDials.Load(), flappy.conns.Load(), malformedSent.Load())
	for _, s := range samples {
		t.Logf("  t=%v goroutines=%d fds=%d heap=%d KiB objects=%d seen=%d users=%d", s.at.Round(time.Second), s.goroutines, s.fds, s.heapAlloc/1024, s.heapObjects, s.seen, s.users)
	}
}

// TestRepeatedClientLifecyclesDoNotLeak starts and stops complete client
// lifecycles against loopback relays and asserts that goroutines, file
// descriptors and heap stay inside fixed tolerances of the pre-run baseline.
func TestRepeatedClientLifecyclesDoNotLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("lifecycle leak check skipped in -short mode")
	}
	const (
		warmupCycles       = 3
		cycles             = 12
		goroutineTolerance = 6
		fdTolerance        = 8
		heapTrendTolerance = 3 << 20
		heapFinalTolerance = 8 << 20
	)

	// Each lifecycle runs as its own subtest: a subtest's cleanups run when it
	// returns, so no client, cache or relay from an earlier cycle is still
	// referenced while later cycles are measured. (Registrating a dozen
	// lifecycles on the parent test would retain them all until the test ends
	// and fake a linear heap leak.)
	runCycle := func(i int) {
		t.Run(fmt.Sprintf("cycle-%02d", i), func(t *testing.T) { runRelayLifecycle(t, i) })
	}
	for i := 0; i < warmupCycles; i++ {
		runCycle(i)
	}
	runtime.GC()
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	goroutinesBefore := runtime.NumGoroutine()
	fdsBefore := fdCount()
	var heapBefore runtime.MemStats
	runtime.ReadMemStats(&heapBefore)

	heaps := make([]uint64, 0, cycles)
	for i := 0; i < cycles; i++ {
		runCycle(warmupCycles + i)
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		heaps = append(heaps, ms.HeapAlloc)
	}

	// Goroutines: poll (with collection) for a return to baseline plus tolerance.
	deadline := time.Now().Add(5 * time.Second)
	goroutinesAfter := runtime.NumGoroutine()
	for {
		runtime.GC()
		goroutinesAfter = runtime.NumGoroutine()
		if goroutinesAfter <= goroutinesBefore+goroutineTolerance || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if goroutinesAfter > goroutinesBefore+goroutineTolerance {
		t.Errorf("goroutine leak across %d lifecycles: %d before, %d after (tolerance +%d)",
			cycles, goroutinesBefore, goroutinesAfter, goroutineTolerance)
	}

	fdsAfter := fdCount()
	if fdsBefore >= 0 && fdsAfter > fdsBefore+fdTolerance {
		t.Errorf("file-descriptor leak across %d lifecycles: %d before, %d after (tolerance +%d)",
			cycles, fdsBefore, fdsAfter, fdTolerance)
	}

	// Heap: the first and last thirds of the cycles must not diverge, and the
	// final value must return to the baseline.
	runtime.GC()
	var heapAfter runtime.MemStats
	runtime.ReadMemStats(&heapAfter)
	third := cycles / 3
	heapEarly := slices.Max(heaps[:third])
	heapLate := slices.Max(heaps[cycles-third:])
	if heapLate > heapEarly+heapTrendTolerance {
		t.Errorf("heap trend across %d lifecycles: first third peaks at %d KiB, last third at %d KiB (tolerance +%d KiB)",
			cycles, heapEarly/1024, heapLate/1024, heapTrendTolerance/1024)
	}
	if heapAfter.HeapAlloc > heapBefore.HeapAlloc+heapFinalTolerance {
		t.Errorf("heap growth across %d lifecycles: %d KiB before, %d KiB after (tolerance +%d KiB)",
			cycles, heapBefore.HeapAlloc/1024, heapAfter.HeapAlloc/1024, heapFinalTolerance/1024)
	}

	heapKiB := make([]int, len(heaps))
	for i, h := range heaps {
		heapKiB[i] = int(h / 1024)
	}
	t.Logf("lifecycle leak check over %d cycles: goroutines %d -> %d (tolerance +%d), fds %d -> %d (tolerance +%d)",
		cycles, goroutinesBefore, goroutinesAfter, goroutineTolerance, fdsBefore, fdsAfter, fdTolerance)
	t.Logf("per-cycle heap after GC (KiB): %v (baseline %d KiB, trend tolerance +%d KiB, final tolerance +%d KiB)",
		heapKiB, heapBefore.HeapAlloc/1024, heapTrendTolerance/1024, heapFinalTolerance/1024)
}

// runRelayLifecycle runs one complete client lifecycle: connect to a fresh
// loopback relay, receive a pushed event, publish one acknowledged message,
// then stop the client and close the relay before returning. Everything is
// torn down by the time it returns, so the caller can measure resource counts.
func runRelayLifecycle(t *testing.T, index int) {
	t.Helper()
	marker := fmt.Sprintf("lifecycle %d", index)
	event := signedEvent(t, marker)
	ts := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, idx int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		relayWriteJSON(ts, conn, relayEventFrame("chat", event))
		for {
			frame, ok := relayNextFrame(ts, conn)
			if !ok {
				return
			}
			var label string
			if len(frame) < 2 || json.Unmarshal(frame[0], &label) != nil || label != "EVENT" {
				continue
			}
			var ev nostr.Event
			if json.Unmarshal(frame[1], &ev) != nil {
				return
			}
			ts.noteEvent(ev)
			if !relayWriteJSON(ts, conn, relayOKFrame(ev.ID, true, "")) {
				return
			}
		}
	})
	h := startRelayHarness(t, relayTestConfig(t, ts.url), nil)
	h.waitRelayStatusCount(ts.url, true, 1)
	h.waitMessage(marker)
	h.send(UserAction{Type: "SEND_MESSAGE", Payload: "lifecycle publish"})
	h.waitForEvent("lifecycle publish acknowledgement", contentPred("acknowledged by 1/1"))
	h.stop()
	ts.srv.Close()
}
