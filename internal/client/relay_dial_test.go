package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRelayDialsAreBoundedAndCancelable(t *testing.T) {
	var active, peak atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		<-release
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	c := transportClient(t, relayTestConfig(t))
	t.Cleanup(func() { close(release) })
	desired := make(map[string][]string)
	for i := range 16 {
		desired[fmt.Sprintf("ws%s/%d", strings.TrimPrefix(server.URL, "http"), i)] = []string{"lobby"}
	}
	c.updateRelaySubscriptions(desired)
	time.Sleep(700 * time.Millisecond)
	if n := peak.Load(); n > 4 || n == 0 {
		t.Errorf("concurrent stalled handshakes = %d, want 1..4", n)
	}
	start := time.Now()
	c.Stop()
	c.wg.Wait()
	if time.Since(start) > time.Second {
		t.Fatal("cancellation waited for a dial permit or handshake")
	}
}
