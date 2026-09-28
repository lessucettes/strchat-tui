package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRelayReconnectsAfterInitialFailure(t *testing.T) {
	var attempts atomic.Int32
	subscribed := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "unavailable", 503)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, msg, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var frame []json.RawMessage
		if json.Unmarshal(msg, &frame) != nil || len(frame) < 3 {
			return
		}
		var label string
		_ = json.Unmarshal(frame[0], &label)
		if label == "REQ" {
			subscribed <- struct{}{}
		}
		for {
			if _, _, err = conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	c, _ := testClient(t)
	c.relays = make(map[string]*managedRelay)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	c.updateRelaySubscriptions(map[string][]string{url: {"lobby"}})
	defer c.shutdown()
	select {
	case <-subscribed:
	case <-time.After(4 * time.Second):
		t.Fatal("initial dial failure was not retried")
	}
}

func TestRelayURLPreservesTransportAndPath(t *testing.T) {
	for _, url := range []string{"ws://127.0.0.1:1234/nostr", "wss://example.com/nostr?network=chat"} {
		got, err := normalizeRelayURL(url)
		if err != nil || got != url {
			t.Errorf("URL %q became %q: %v", url, got, err)
		}
	}
}

func TestShutdownWithBlockedDisplay(t *testing.T) {
	c, _ := testClient(t)
	c.eventsChan = make(chan DisplayEvent)
	c.actionsChan = make(chan UserAction)
	done := make(chan struct{})
	go func() { c.Run(); close(done) }()
	c.cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked on absent display consumer")
	}
}

func TestRelayReplacementJoinsRetiredWorker(t *testing.T) {
	c, _ := testClient(t)
	ctx, cancel := context.WithCancel(c.ctx)
	retired := make(chan struct{})
	c.relays["old"] = &managedRelay{url: "old", cancel: cancel, done: retired}
	updated := make(chan struct{})
	go func() { c.updateRelaySubscriptions(nil); close(updated) }()
	<-ctx.Done()
	select {
	case <-updated:
		close(retired)
		t.Fatal("removed worker was not joined before replacement could start")
	case <-time.After(20 * time.Millisecond):
	}
	close(retired)
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("subscription update did not resume after retired worker exited")
	}
}
