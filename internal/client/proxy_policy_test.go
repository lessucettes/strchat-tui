package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSOCKSEndpointValidation(t *testing.T) {
	for _, address := range []string{"127.0.0.1:9050", "socks5://127.0.0.1:9050", "socks5h://localhost:9050", "[::1]:9050"} {
		client, err := newRelayHTTPClient(address)
		if err != nil || client == nil {
			t.Fatalf("valid endpoint %q: %v", address, err)
		}
		client.CloseIdleConnections()
	}
	if client, err := newRelayHTTPClient(""); client != nil || err != nil {
		t.Fatal("an empty option must preserve the default transport")
	}
	for _, address := range []string{
		" ",
		"http://127.0.0.1:9050", "socks4://127.0.0.1:9050", "127.0.0.1", ":9050",
		"127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:port", "::1:9050",
		"socks5://127.0.0.1:9050/", "socks5://127.0.0.1:9050?", "socks5://127.0.0.1:9050#",
		"socks5://test:private-marker@127.0.0.1:9050", "socks5://test:private-marker%GG@127.0.0.1:9050",
	} {
		client, err := newRelayHTTPClient(address)
		if err == nil || client != nil {
			t.Errorf("invalid SOCKS5 endpoint was accepted: %q", address)
		}
		if err != nil && strings.Contains(err.Error(), "private-marker") {
			t.Error("proxy validation exposed credentials")
		}
	}
}

func TestSOCKSOnionNameIsResolvedByProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			defer conn.CloseNow()
			conn.Write(r.Context(), websocket.MessageText, []byte("onion-tunnel"))
			conn.Read(r.Context())
		}
	}))
	defer server.Close()
	address, requests := startTestSOCKS(t, server.Listener.Addr().String(), "forward")
	c := proxyTestClient(t, "socks5h://"+address)
	var lookups atomic.Int32
	resolver := &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		lookups.Add(1)
		return nil, errors.New("local destination DNS is forbidden")
	}}
	c.relayHTTPClient.Transport.(*http.Transport).DialContext = (&net.Dialer{Resolver: resolver}).DialContext
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := c.dialRelay(ctx, "ws://relay-test.onion/socket")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	request := <-requests
	if request.atyp != 3 || request.host != "relay-test.onion" || request.port != 80 || lookups.Load() != 0 {
		t.Fatalf("onion name was not passed intact to SOCKS5: %+v, local DNS=%d", request, lookups.Load())
	}
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "onion-tunnel" {
		t.Fatalf("onion WebSocket traffic failed: %q, %v", data, err)
	}
}

func TestSOCKSFailureNeverFallsBackToDirect(t *testing.T) {
	for _, mode := range []string{"reject", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var direct atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				direct.Add(1)
				http.Error(w, "must not be reached", http.StatusForbidden)
			}))
			defer server.Close()
			address, _ := startTestSOCKS(t, server.Listener.Addr().String(), mode)
			if mode == "unavailable" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				address = listener.Addr().String()
				listener.Close()
			}
			c := proxyTestClient(t, address)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, _, err := c.dialRelay(ctx, "ws"+strings.TrimPrefix(server.URL, "http"))
			if conn != nil {
				conn.CloseNow()
			}
			if err == nil || direct.Load() != 0 {
				t.Fatalf("failed SOCKS5 proxy was bypassed: error=%v, direct requests=%d", err, direct.Load())
			}
		})
	}
}

func TestSOCKSStalledHandshakeIsCancelable(t *testing.T) {
	address, _ := startTestSOCKS(t, "", "stall")
	c := proxyTestClient(t, address)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		conn, _, err := c.dialRelay(ctx, "ws://relay-test.onion")
		if conn != nil {
			conn.CloseNow()
		}
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("proxy handshake did not preserve cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceling a stalled SOCKS5 handshake did not release the dial")
	}
}
