package client

// gorilla/websocket relay transport. Gorilla has no context-aware Read/Write
// and its Ping does not wait for a pong, so this adapter supplies those
// contracts explicitly and keeps the relay worker independent of the backend.
//
// Concurrency: gorilla permits one reader and one writer plus concurrent
// WriteControl. Ping therefore uses WriteControl while the worker's writer owns
// data frames. Cancellation closes the underlying socket; it never mutates
// gorilla's unsynchronized writeDeadline field from another goroutine.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Keep per-socket gorilla buffering small: a group can hold many relays.
	gorillaReadBufferSize  = 4096
	gorillaWriteBufferSize = 4096
	// maxScratchBytes bounds the lazy per-connection receive scratch space.
	// The wire limit is maxFrameBytes; bytes.Buffer may round a one-byte
	// overflow up to the next power of two, hence the explicit 2x ceiling.
	maxScratchBytes = 2 * maxFrameBytes
	// pingTokenBytes is small enough for any control frame and large enough
	// that unsolicited pongs cannot guess a live token.
	pingTokenBytes = 8
)

var errRelayFrameTooLarge = errors.New("relay message exceeds receive limit")

// The selected production backend must satisfy the worker contract.
var _ relayConn = (*gorillaRelayConn)(nil)

func dialRelayTransport(ctx context.Context, rawURL string, httpClient *http.Client, timeout time.Duration) (relayConn, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid relay URL")
	}
	switch strings.ToLower(target.Scheme) {
	case "http":
		target.Scheme = "ws"
	case "https":
		target.Scheme = "wss"
	case "ws", "wss":
	default:
		return nil, errors.New("invalid relay URL")
	}
	if target.Hostname() == "" {
		return nil, errors.New("invalid relay URL")
	}
	settings, err := relayDialSettingsFor(httpClient)
	if err != nil {
		return nil, err
	}
	for redirects := 0; ; redirects++ {
		conn, resp, dialErr := dialGorilla(ctx, target, settings, timeout)
		if dialErr == nil {
			return newGorillaRelayConn(conn), nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if resp == nil {
			return nil, dialErr
		}
		location := resp.Header.Get("Location")
		status := resp.StatusCode
		if resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
		}
		if !isWSRedirect(status) || location == "" {
			return nil, fmt.Errorf("relay websocket handshake failed with HTTP status %d", status)
		}
		if redirects >= maxRelayRedirects {
			return nil, errors.New("relay redirected too many times")
		}
		next, redirectErr := resolveWSRedirect(target, location)
		if redirectErr != nil {
			return nil, redirectErr
		}
		// Never downgrade an authenticated TLS relay to cleartext.
		if target.Scheme == "wss" && next.Scheme == "ws" {
			return nil, errors.New("relay redirect attempted a TLS downgrade")
		}
		target = next
	}
}

// relayDialSettings is the dialing subset of an *http.Transport: how to reach
// a host, which proxy applies to a request, and which TLS trust applies. A nil
// proxy function means "no proxy"; a function returning nil means "direct".
type relayDialSettings struct {
	dialContext func(context.Context, string, string) (net.Conn, error)
	proxy       func(*http.Request) (*url.URL, error)
	tlsConfig   *tls.Config
}

func relayDialSettingsFor(httpClient *http.Client) (relayDialSettings, error) {
	settings := relayDialSettings{
		dialContext: (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext,
		proxy:       http.ProxyFromEnvironment,
	}
	if httpClient == nil || httpClient.Transport == nil {
		return settings, nil
	}
	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		return settings, errors.New("relay HTTP transport does not support websocket dialing")
	}
	if transport.DialContext != nil {
		settings.dialContext = transport.DialContext
	}
	// nil means the transport was deliberately configured without a proxy;
	// preserve that instead of falling back to the environment.
	settings.proxy = transport.Proxy
	settings.tlsConfig = transport.TLSClientConfig
	return settings, nil
}

func (s relayDialSettings) proxyFor(target *url.URL) (*url.URL, error) {
	if s.proxy == nil {
		return nil, nil
	}
	requestURL := *target
	switch requestURL.Scheme {
	case "ws":
		requestURL.Scheme = "http"
	case "wss":
		requestURL.Scheme = "https"
	}
	return s.proxy(&http.Request{URL: &requestURL, Host: requestURL.Host})
}

func dialGorilla(ctx context.Context, target *url.URL, settings relayDialSettings, timeout time.Duration) (*websocket.Conn, *http.Response, error) {
	tlsConfig := settings.tlsConfig
	if tlsConfig != nil {
		// RFC 6455 uses an HTTP/1.1 upgrade, even if this shared transport's
		// ordinary requests negotiate HTTP/2. Never mutate the caller's config.
		tlsConfig = tlsConfig.Clone()
		tlsConfig.NextProtos = []string{"http/1.1"}
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout: timeout,
		ReadBufferSize:   gorillaReadBufferSize,
		WriteBufferSize:  gorillaWriteBufferSize,
		TLSClientConfig:  tlsConfig,
	}
	// gorilla performs the HTTP upgrade (and TLS) after NetDialContext returns,
	// using socket deadlines only. Closing the socket on caller cancellation
	// keeps stalled handshakes immediately cancelable like coder's dialer.
	var stopWatch func() bool
	dialer.NetDialContext = func(dialCtx context.Context, network, _ string) (net.Conn, error) {
		conn, err := dialRelayNet(dialCtx, target, settings)
		if err != nil {
			return nil, err
		}
		stopWatch = context.AfterFunc(ctx, func() { _ = conn.Close() })
		return conn, nil
	}
	conn, resp, err := dialer.DialContext(ctx, target.String(), nil)
	if stopWatch != nil {
		stopWatch()
	}
	if err == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = conn.Close()
			return nil, resp, ctxErr
		}
	}
	return conn, resp, err
}

// dialRelayNet opens a socket to the relay, routing through the exact proxy
// the runtime HTTP client describes. Every proxy failure is returned; there is
// no fallback to a direct dial.
func dialRelayNet(ctx context.Context, target *url.URL, settings relayDialSettings) (net.Conn, error) {
	proxyURL, err := settings.proxyFor(target)
	if err != nil {
		return nil, err
	}
	destination := relayHostPort(target)
	if proxyURL == nil {
		return settings.dialContext(ctx, "tcp", destination)
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "socks5", "socks5h":
		return dialSOCKS5Proxy(ctx, settings.dialContext, proxyURL, destination)
	case "http":
		return dialHTTPProxy(ctx, settings.dialContext, proxyURL, destination, nil)
	case "https":
		return dialHTTPProxy(ctx, settings.dialContext, proxyURL, destination, settings.tlsConfig)
	default:
		return nil, fmt.Errorf("relay proxy scheme %q is not supported", proxyURL.Scheme)
	}
}

func relayHostPort(target *url.URL) string {
	port := target.Port()
	if port == "" {
		switch strings.ToLower(target.Scheme) {
		case "wss", "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(target.Hostname(), port)
}

// dialSOCKS5Proxy performs the RFC 1928 handshake and, when the runtime client
// carries credentials, RFC 1929 username/password authentication. Destination
// names are always forwarded to the proxy; neither socks5 nor socks5h resolves
// them locally.
func dialSOCKS5Proxy(ctx context.Context, base func(context.Context, string, string) (net.Conn, error), proxyURL *url.URL, destination string) (net.Conn, error) {
	if proxyURL.Hostname() == "" {
		return nil, errors.New("relay SOCKS5 proxy has no host")
	}
	host, portText, err := net.SplitHostPort(destination)
	if err != nil {
		return nil, errors.New("relay destination address is invalid")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("relay destination port is invalid")
	}
	raw, err := base(ctx, "tcp", relayHostPort(proxyURL))
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	ok := false
	defer func() {
		stop()
		if !ok {
			_ = raw.Close()
		}
	}()
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		_ = raw.SetDeadline(deadline)
	}
	if err := socks5Handshake(ctx, raw, proxyURL, host, uint16(port)); err != nil {
		return nil, err
	}
	stop()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	ok = true
	return raw, nil
}

func socks5Handshake(ctx context.Context, conn net.Conn, proxyURL *url.URL, host string, port uint16) error {
	hasAuth := proxyURL.User != nil
	greeting := []byte{5, 1, 0}
	if hasAuth {
		greeting = []byte{5, 2, 0, 2}
	}
	if err := writeAll(conn, greeting); err != nil {
		return proxyError(ctx, err)
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		return proxyError(ctx, err)
	}
	if method[0] != 5 {
		return errors.New("relay SOCKS5 proxy returned an invalid version")
	}
	switch method[1] {
	case 0:
	case 2:
		if !hasAuth {
			return errors.New("relay SOCKS5 proxy requires unsupported authentication")
		}
		user := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		if len(user) > 255 || len(password) > 255 {
			return errors.New("relay SOCKS5 proxy credentials are too long")
		}
		auth := []byte{1, byte(len(user))}
		auth = append(auth, user...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if err := writeAll(conn, auth); err != nil {
			return proxyError(ctx, err)
		}
		var reply [2]byte
		if _, err := io.ReadFull(conn, reply[:]); err != nil {
			return proxyError(ctx, err)
		}
		if reply[0] != 1 || reply[1] != 0 {
			return errors.New("relay SOCKS5 proxy rejected authentication")
		}
	default:
		return errors.New("relay SOCKS5 proxy offers no supported authentication method")
	}
	request := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 1)
			request = append(request, ip4...)
		} else {
			request = append(request, 4)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return errors.New("relay SOCKS5 destination host is invalid")
		}
		// Address type 3 forwards the name unchanged; no local DNS lookup.
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], port)
	request = append(request, portBytes[:]...)
	if err := writeAll(conn, request); err != nil {
		return proxyError(ctx, err)
	}
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return proxyError(ctx, err)
	}
	if reply[0] != 5 || reply[2] != 0 {
		return errors.New("relay SOCKS5 proxy returned an invalid response")
	}
	if reply[1] != 0 {
		return fmt.Errorf("relay SOCKS5 proxy refused the connection (code %d)", reply[1])
	}
	var err error
	switch reply[3] {
	case 1:
		_, err = io.CopyN(io.Discard, conn, 4+2)
	case 4:
		_, err = io.CopyN(io.Discard, conn, 16+2)
	case 3:
		var size [1]byte
		if _, err = io.ReadFull(conn, size[:]); err == nil {
			_, err = io.CopyN(io.Discard, conn, int64(size[0])+2)
		}
	default:
		return errors.New("relay SOCKS5 proxy returned an invalid address type")
	}
	if err != nil {
		return proxyError(ctx, err)
	}
	return nil
}

// dialHTTPProxy establishes an HTTP CONNECT tunnel. An https proxy is wrapped
// in TLS before the CONNECT request, mirroring net/http's Transport; a plain
// CONNECT to an https proxy would leak the request in cleartext.
func dialHTTPProxy(ctx context.Context, base func(context.Context, string, string) (net.Conn, error), proxyURL *url.URL, destination string, tlsConfig *tls.Config) (net.Conn, error) {
	if proxyURL.Hostname() == "" {
		return nil, errors.New("relay proxy has no host")
	}
	if strings.ContainsAny(destination, "\r\n") {
		return nil, errors.New("relay destination contains invalid characters")
	}
	raw, err := base(ctx, "tcp", relayHostPort(proxyURL))
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	ok := false
	defer func() {
		stop()
		if !ok {
			_ = raw.Close()
		}
	}()
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		_ = raw.SetDeadline(deadline)
	}
	conn := net.Conn(raw)
	if strings.EqualFold(proxyURL.Scheme, "https") {
		cfg := &tls.Config{}
		if tlsConfig != nil {
			cfg = tlsConfig.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = proxyURL.Hostname()
		}
		// The proxy speaks HTTP/1.1 CONNECT after the handshake, never h2.
		cfg.NextProtos = []string{"http/1.1"}
		tlsConn := tls.Client(raw, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, proxyError(ctx, err)
		}
		conn = tlsConn
	}
	var request strings.Builder
	fmt.Fprintf(&request, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", destination, destination)
	if user := proxyURL.User; user != nil {
		password, _ := user.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(user.Username() + ":" + password))
		fmt.Fprintf(&request, "Proxy-Authorization: Basic %s\r\n", credentials)
	}
	request.WriteString("\r\n")
	if err := writeAll(conn, []byte(request.String())); err != nil {
		return nil, proxyError(ctx, err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, proxyError(ctx, err)
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relay proxy CONNECT failed with HTTP status %d", response.StatusCode)
	}
	stop()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	ok = true
	if reader.Buffered() > 0 {
		return &relayBufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

type relayBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *relayBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func proxyError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		p = p[n:]
	}
	return nil
}

type gorillaRelayConn struct {
	conn      *websocket.Conn
	closeOnce sync.Once

	// scratch is owned by the single reader goroutine. It starts empty and
	// grows to the largest message seen, so repeated long frames reuse it
	// instead of allocating a fresh growing message buffer each time.
	scratch bytes.Buffer

	pongMu sync.Mutex
	pongs  map[string]chan struct{}
}

func newGorillaRelayConn(conn *websocket.Conn) *gorillaRelayConn {
	c := &gorillaRelayConn{conn: conn, pongs: make(map[string]chan struct{})}
	conn.SetPongHandler(func(appData string) error {
		c.pongMu.Lock()
		waiter := c.pongs[appData]
		if waiter != nil {
			delete(c.pongs, appData)
		}
		c.pongMu.Unlock()
		if waiter != nil {
			close(waiter)
		}
		return nil
	})
	return c
}

func (c *gorillaRelayConn) Read(ctx context.Context) (relayMessageType, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	kind, reader, err := c.conn.NextReader()
	if err != nil {
		return 0, nil, readContextError(ctx, err)
	}
	c.scratch.Reset()
	n, err := c.scratch.ReadFrom(io.LimitReader(reader, maxFrameBytes+1))
	if err != nil {
		return 0, nil, readContextError(ctx, err)
	}
	if n > maxFrameBytes || c.scratch.Cap() > maxScratchBytes {
		return 0, nil, errRelayFrameTooLarge
	}
	// The reader goroutine hands the bytes to the serve loop, so the scratch
	// buffer cannot be reused for the next message until this clone is made.
	data := bytes.Clone(c.scratch.Bytes())
	if kind == websocket.TextMessage {
		return relayMessageText, data, nil
	}
	return relayMessageBinary, data, nil
}

func readContextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func (c *gorillaRelayConn) Write(ctx context.Context, typ relayMessageType, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(connectTimeout)
	}
	// Only the writer goroutine touches gorilla's write deadline field.
	_ = c.conn.SetWriteDeadline(deadline)
	// A deadline set by a cancellation callback can be overwritten by the
	// writer's later deadline setup. Close instead: an interrupted gorilla
	// write cannot safely reuse its connection, and Close cannot be undone.
	stop := context.AfterFunc(ctx, func() {
		_ = c.CloseNow()
	})
	defer stop()
	kind := websocket.BinaryMessage
	if typ == relayMessageText {
		kind = websocket.TextMessage
	}
	err := c.conn.WriteMessage(kind, data)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return err
}

func (c *gorillaRelayConn) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(connectTimeout)
	}
	var token [pingTokenBytes]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fmt.Errorf("relay ping token: %w", err)
	}
	key := string(token[:])
	waiter := make(chan struct{})
	c.pongMu.Lock()
	c.pongs[key] = waiter
	c.pongMu.Unlock()
	defer func() {
		c.pongMu.Lock()
		if c.pongs[key] == waiter {
			delete(c.pongs, key)
		}
		c.pongMu.Unlock()
	}()
	if err := c.conn.WriteControl(websocket.PingMessage, token[:], deadline); err != nil {
		return proxyError(ctx, err)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-waiter:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("relay ping timed out waiting for pong")
	}
}

func (c *gorillaRelayConn) SetReadLimit(n int64) { c.conn.SetReadLimit(n) }

// CloseNow closes the underlying transport without a WebSocket close
// handshake, matching coder's CloseNow contract. It is idempotent and safe
// concurrently with Read, Write and Ping.
func (c *gorillaRelayConn) CloseNow() error {
	var err error
	c.closeOnce.Do(func() { err = c.conn.UnderlyingConn().Close() })
	return err
}
