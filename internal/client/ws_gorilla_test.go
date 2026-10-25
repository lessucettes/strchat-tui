package client

// Transport-level regression tests for the gorilla adapter. These deliberately
// exercise the areas the earlier prototype got wrong: pong correlation, HTTPS
// proxy tunnelling and cancellation of a blocked write. The write test is
// meaningful under -race: the rejected implementation mutated gorilla's
// unsynchronized writeDeadline field from context.AfterFunc.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	gorillaws "github.com/gorilla/websocket"
)

// TestGorillaPingWaitsForMatchingPong proves an unsolicited or empty pong does
// not satisfy a ping. The relay sends a zero-length pong and a bogus token
// first, then the exact token only after a delay.
func TestGorillaPingWaitsForMatchingPong(t *testing.T) {
	upgrader := gorillaws.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetPingHandler(func(appData string) error {
			_ = conn.WriteControl(gorillaws.PongMessage, nil, time.Now().Add(time.Second))
			_ = conn.WriteControl(gorillaws.PongMessage, []byte("bogus-token"), time.Now().Add(time.Second))
			time.Sleep(150 * time.Millisecond)
			return conn.WriteControl(gorillaws.PongMessage, []byte(appData), time.Now().Add(time.Second))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialRelayTransport(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil, connectTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		conn.CloseNow()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Error("reader did not exit after CloseNow")
		}
	})

	start := time.Now()
	pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pingCancel()
	if err := conn.Ping(pingCtx); err != nil {
		t.Fatalf("ping with a matching pong failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("ping returned after %v, before the matching pong: an unsolicited pong was accepted", elapsed)
	}
}

// TestGorillaHTTPSProxyTunnel covers HTTP and HTTPS CONNECT proxies with both
// plain and TLS relay targets. An https proxy must be reached over TLS before
// CONNECT; the prototype tried a cleartext CONNECT and gorilla rejected the
// https scheme outright.
func TestGorillaHTTPSProxyTunnel(t *testing.T) {
	for _, proxySecure := range []bool{false, true} {
		for _, targetSecure := range []bool{false, true} {
			name := "proxy_http"
			if proxySecure {
				name = "proxy_https"
			}
			name += "/target_wss"
			if !targetSecure {
				name = strings.TrimSuffix(name, "wss") + "ws"
			}
			t.Run(name, func(t *testing.T) {
				target, requests := wsTestEchoServer(t, targetSecure)
				proxy, connects := wsTestCONNECTProxy(t, proxySecure, "")

				transport := http.DefaultTransport.(*http.Transport).Clone()
				proxyURL, err := url.Parse(proxy.URL)
				if err != nil {
					t.Fatal(err)
				}
				transport.Proxy = http.ProxyURL(proxyURL)
				roots := x509.NewCertPool()
				needRoots := false
				if proxySecure {
					roots.AddCert(proxy.Certificate())
					needRoots = true
				}
				if targetSecure {
					roots.AddCert(target.Certificate())
					needRoots = true
				}
				if needRoots {
					transport.TLSClientConfig = &tls.Config{RootCAs: roots}
				}

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				targetURL := "ws" + strings.TrimPrefix(target.URL, "http")
				if targetSecure {
					targetURL = "wss" + strings.TrimPrefix(target.URL, "https")
				}
				conn, err := dialRelayTransport(ctx, targetURL, &http.Client{Transport: transport}, 2*time.Second)
				if err != nil {
					t.Fatalf("dial through %s proxy: %v", proxyURL.Scheme, err)
				}
				defer conn.CloseNow()
				kind, data, err := conn.Read(ctx)
				if err != nil || kind != relayMessageText || string(data) != "through-proxy" {
					t.Fatalf("relay frame through proxy: kind=%v data=%q err=%v", kind, data, err)
				}
				if got := connects.Load(); got != 1 {
					t.Fatalf("proxy saw %d CONNECT requests, want 1", got)
				}
				if requests.Load() == 0 {
					t.Fatal("target relay was never reached")
				}
			})
		}
	}
}

func TestGorillaHTTPSProxyUsesTLSWithoutCustomConfig(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	firstByte := make(chan byte, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var first [1]byte
		if _, err := io.ReadFull(conn, first[:]); err == nil {
			firstByte <- first[0]
		}
	}()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = nil // default system trust, not a custom CA config
	proxyURL, err := url.Parse("https://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	transport.Proxy = http.ProxyURL(proxyURL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialRelayTransport(ctx, "ws://relay.invalid", &http.Client{Transport: transport}, time.Second)
	if err == nil {
		conn.CloseNow()
		t.Fatal("probe proxy unexpectedly completed a handshake")
	}
	<-done
	select {
	case first := <-firstByte:
		if first != 0x16 {
			t.Fatalf("HTTPS proxy received first byte %#x, want a TLS handshake, never cleartext CONNECT", first)
		}
	default:
		t.Fatal("probe proxy received no handshake bytes")
	}
}

func TestGorillaProxySchemeFailsClosed(t *testing.T) {
	target, requests := wsTestEchoServer(t, false)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		proxyURL, err := url.Parse("ftp://127.0.0.1:1")
		return proxyURL, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if conn, err := dialRelayTransport(ctx, "ws"+strings.TrimPrefix(target.URL, "http"), &http.Client{Transport: transport}, time.Second); err == nil {
		conn.CloseNow()
		t.Fatal("unsupported proxy scheme unexpectedly connected")
	}
	if requests.Load() != 0 {
		t.Fatalf("unsupported proxy fell back to a direct dial: %d target requests", requests.Load())
	}
}

// TestGorillaWriteCancellationUnblocksStalledPeer writes far more than the
// socket buffers while the peer reads nothing, then cancels. It must return
// promptly with context.Canceled. Under -race this also catches a concurrent
// mutation of gorilla's writeDeadline field.
func TestGorillaWriteCancellationUnblocksStalledPeer(t *testing.T) {
	release := make(chan struct{})
	upgrader := gorillaws.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialRelayTransport(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil, connectTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	writeCtx, writeCancel := context.WithCancel(context.Background())
	defer writeCancel()
	done := make(chan error, 1)
	go func() {
		done <- conn.Write(writeCtx, relayMessageText, bytes.Repeat([]byte("w"), 16<<20))
	}()
	time.Sleep(250 * time.Millisecond)
	start := time.Now()
	writeCancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled write reported success")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled write returned %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("canceled write took %v to return", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled write stayed blocked on a stalled peer")
	}
}

// deadlineRaceConn holds the writer just before its socket deadline is set,
// allowing cancellation to happen before that deadline could overwrite it.
type deadlineRaceConn struct {
	net.Conn
	armed                       atomic.Bool
	trap, observed              sync.Once
	entered, release, cancelled chan struct{}
}

func (c *deadlineRaceConn) SetWriteDeadline(deadline time.Time) error {
	if c.armed.Load() && deadline.After(time.Now().Add(time.Second)) {
		c.trap.Do(func() { close(c.entered); <-c.release })
	}
	err := c.Conn.SetWriteDeadline(deadline)
	if c.armed.Load() && !deadline.After(time.Now().Add(time.Second)) {
		c.observed.Do(func() { close(c.cancelled) })
	}
	return err
}

func (c *deadlineRaceConn) Close() error {
	if c.armed.Load() {
		c.observed.Do(func() { close(c.cancelled) })
	}
	return c.Conn.Close()
}

func TestGorillaWriteCancellationCannotBeOverwrittenByDeadline(t *testing.T) {
	releasePeer := make(chan struct{})
	upgrader := gorillaws.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		<-releasePeer
	}))
	defer server.Close()
	defer close(releasePeer)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	var trapped *deadlineRaceConn
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		trapped = &deadlineRaceConn{Conn: conn, entered: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
		return trapped, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialRelayTransport(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &http.Client{Transport: transport}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	trapped.armed.Store(true)
	writeCtx, cancelWrite := context.WithCancel(context.Background())
	defer cancelWrite()
	done := make(chan error, 1)
	go func() { done <- conn.Write(writeCtx, relayMessageText, []byte("cancel-before-wire")) }()
	select {
	case <-trapped.entered:
	case <-ctx.Done():
		t.Fatal("writer did not reach deadline setup")
	}
	cancelWrite()
	select {
	case <-trapped.cancelled:
	case <-ctx.Done():
		t.Fatal("cancellation did not interrupt transport")
	}
	close(trapped.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled pre-write returned %v, want context.Canceled", err)
		}
	case <-ctx.Done():
		t.Fatal("cancelled write did not return")
	}
}

// wsTestEchoServer accepts one websocket at a time, sends a marker and waits
// for the client to leave.
func wsTestEchoServer(t *testing.T, secure bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = conn.Write(ctx, coderws.MessageText, []byte("through-proxy"))
		_, _, _ = conn.Read(ctx)
	})
	var server *httptest.Server
	if secure {
		server = httptest.NewTLSServer(handler)
	} else {
		server = httptest.NewServer(handler)
	}
	t.Cleanup(server.Close)
	return server, requests
}

// wsTestCONNECTProxy is a minimal CONNECT tunnel, optionally served over TLS.
func wsTestCONNECTProxy(t *testing.T, secure bool, expectedAuth string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	connects := &atomic.Int32{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		if expectedAuth != "" && r.Header.Get("Proxy-Authorization") != expectedAuth {
			http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
			return
		}
		connects.Add(1)
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			upstream.Close()
			http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
			return
		}
		client, _, err := hijacker.Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() {
			defer client.Close()
			defer upstream.Close()
			go func() {
				_, _ = io.Copy(upstream, client)
				_ = upstream.Close()
			}()
			_, _ = io.Copy(client, upstream)
		}()
	})
	var server *httptest.Server
	if secure {
		server = httptest.NewTLSServer(handler)
	} else {
		server = httptest.NewServer(handler)
	}
	t.Cleanup(server.Close)
	return server, connects
}
