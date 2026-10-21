package client

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Opt in because the real default dial pacing makes a full safety-ceiling
// exercise take tens of seconds. All sockets stay on one loopback server.
func TestRelayGroupSafetyCeilingLoopback(t *testing.T) {
	if os.Getenv("STRCHAT_RELAY_SCALE") == "" {
		t.Skip("set STRCHAT_RELAY_SCALE=1 to exercise the full connection ceiling")
	}
	server := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
		if !relayTestWaitREQ(ts, conn) {
			return
		}
		// The ordinary fixture's 15s read deadline is shorter than a paced
		// full-group startup. Keep early sockets alive for the whole exercise.
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				ts.noteClose(err)
				return
			}
		}
	})
	var chats []string
	plans := make(map[string][][]string)
	for _, cell := range "0123456789bcdefghjkm" {
		chat := string(cell) + "00000"
		chats = append(chats, chat)
		lists := make([][]string, 2)
		for family := range lists {
			for rank := range defaultRelayCount {
				lists[family] = append(lists[family], fmt.Sprintf("%s/geo/%s/%d/%d", server.url, chat, family, rank))
			}
		}
		plans[chat] = lists
	}
	if len(chats) != 20 {
		t.Fatalf("fixture has %d chats, want 20", len(chats))
	}
	cfg := relayTestConfig(t)
	cfg.Views = []View{{Name: "world", IsGroup: true, Children: chats}}
	cfg.ActiveViewName = "world"
	for i := range maxActiveRelays - len(chats)*2*defaultRelayCount {
		cfg.AnchorRelays = append(cfg.AnchorRelays, fmt.Sprintf("%s/anchor/%d", server.url, i))
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutinesBefore := runtime.NumGoroutine()
	fdsBefore := relayScaleFDCount(t)
	start := time.Now()
	h := startRelayHarness(t, cfg, func(c *client) { c.geoPlans = plans })
	server.waitREQs(int32(maxActiveRelays), time.Minute)
	if !waitUntil(t, transportWaitOp, func() bool {
		statuses, _ := h.latestRelayStatuses()
		if len(statuses) != maxActiveRelays {
			return false
		}
		for _, status := range statuses {
			if !status.Connected {
				return false
			}
		}
		return true
	}) {
		t.Fatal("not all primary sockets became connected")
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("loopback ceiling=%d, geo chats=%d, startup=%s, heap delta=%.2f MiB, goroutine delta=%d, fd delta=%d (includes test server and harness)",
		maxActiveRelays, len(chats), time.Since(start).Round(time.Millisecond),
		float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/(1<<20),
		runtime.NumGoroutine()-goroutinesBefore, relayScaleFDCount(t)-fdsBefore)
	stop := time.Now()
	h.stop()
	if !waitUntil(t, 3*time.Second, func() bool {
		return runtime.NumGoroutine() <= goroutinesBefore+16 && relayScaleFDCount(t) <= fdsBefore+8
	}) {
		t.Fatalf("resources did not settle after shutdown: goroutines=%d baseline=%d fds=%d baseline=%d",
			runtime.NumGoroutine(), goroutinesBefore, relayScaleFDCount(t), fdsBefore)
	}
	t.Logf("shutdown and resource settling=%s", time.Since(stop).Round(time.Millisecond))
}

func relayScaleFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("descriptor measurement requires /proc/self/fd")
	}
	return len(entries)
}
