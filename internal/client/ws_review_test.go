package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
)

// Check the real tunnel, not a reconstructed format string from redacted logs.
func TestGorillaAuthenticatedProxyTunnel(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			target, _ := wsTestEchoServer(t, false)
			expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-test:proxy-fixture"))
			proxy, connects := wsTestCONNECTProxy(t, secure, expected)
			proxyURL, err := url.Parse(proxy.URL)
			if err != nil {
				t.Fatal(err)
			}
			proxyURL.User = url.UserPassword("proxy-test", "proxy-fixture")
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = http.ProxyURL(proxyURL)
			if secure {
				roots := x509.NewCertPool()
				roots.AddCert(proxy.Certificate())
				transport.TLSClientConfig = &tls.Config{RootCAs: roots}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := dialRelayTransport(ctx, "ws"+strings.TrimPrefix(target.URL, "http"), &http.Client{Transport: transport}, time.Second)
			if err != nil {
				t.Fatal("authenticated proxy tunnel failed:", err)
			}
			defer conn.CloseNow()
			_, data, err := conn.Read(ctx)
			if err != nil || string(data) != "through-proxy" || connects.Load() != 1 {
				t.Fatal("authenticated proxy did not deliver the relay frame")
			}
		})
	}
}

func TestGorillaRejectsEmptyHostnameBeforeDial(t *testing.T) {
	var dials atomic.Int32
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("isolated hostname probe")
	}
	for _, raw := range []string{"ws://:8080/x", "wss://:8080/x"} {
		conn, err := dialRelayTransport(context.Background(), raw, &http.Client{Transport: transport}, time.Second)
		if conn != nil {
			conn.CloseNow()
		}
		if err == nil || dials.Load() != 0 {
			t.Fatalf("malformed hostname reached dialer: err=%v calls=%d", err, dials.Load())
		}
	}
	base, err := url.Parse("ws://relay.invalid/path")
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{"ws://:8080/x", "//:8080/x", "https://:8080/x"} {
		if _, err := resolveWSRedirect(base, location); err == nil {
			t.Fatalf("accepted a redirect with an empty hostname: %q", location)
		}
	}
}

type cancelAtHandshakeClearConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *cancelAtHandshakeClearConn) SetDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		c.cancel()
	}
	return c.Conn.SetDeadline(deadline)
}

func TestGorillaCancellationAtHandshakeCompletion(t *testing.T) {
	target, _ := wsTestEchoServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
		if err != nil {
			return nil, err
		}
		return &cancelAtHandshakeClearConn{Conn: conn, cancel: cancel}, nil
	}
	conn, err := dialRelayTransport(ctx, "ws"+strings.TrimPrefix(target.URL, "http"), &http.Client{Transport: transport}, time.Second)
	if conn != nil {
		conn.CloseNow()
	}
	if conn != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled handshake returned a connection=%v error=%v", conn != nil, err)
	}
}

func TestGorillaRelayTLSUsesHTTP1WithoutMutatingConfig(t *testing.T) {
	upgrader := gorillaws.Upgrader{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(gorillaws.TextMessage, []byte("http1-upgrade"))
		_, _, _ = conn.ReadMessage()
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	config := &tls.Config{RootCAs: roots, NextProtos: []string{"h2", "http/1.1"}}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = config
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialRelayTransport(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &http.Client{Transport: transport}, time.Second)
	if err != nil {
		t.Fatal("HTTP/1.1 websocket upgrade with a shared TLS config failed:", err)
	}
	defer conn.CloseNow()
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "http1-upgrade" {
		t.Fatal("HTTP/1.1 websocket did not deliver its frame")
	}
	if strings.Join(config.NextProtos, ",") != "h2,http/1.1" {
		t.Fatal("dialer mutated the shared TLS configuration")
	}
}
