package client

import (
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRelayLatencyExcludesDialQueue(t *testing.T) {
	server := newRelayWSServer(t, func(ts *relayTestServer, conn *websocket.Conn, _ int) {
		relayServeAckFrames(ts, conn, true, "")
	})
	c := transportClient(t, relayTestConfig(t))
	c.relayTiming.dialGap = 300 * time.Millisecond
	c.updateRelaySubscriptions(map[string][]string{
		server.url + "/one": {"lobby"},
		server.url + "/two": {"lobby"},
	})
	for range 2 {
		select {
		case report := <-c.incoming:
			if !report.connected {
				t.Fatal("loopback connection failed")
			}
			if report.latency >= 200*time.Millisecond {
				t.Fatalf("handshake latency %s includes waiting for dial pacing", report.latency)
			}
		case <-time.After(transportWaitOp):
			t.Fatal("queued dial never connected")
		}
	}
}
