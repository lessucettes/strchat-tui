package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// relayMessageType mirrors the two RFC 6455 data message types the relay
// protocol uses. The adapter translates its backend's message-kind constants
// so the worker never depends on the selected WebSocket implementation.
type relayMessageType uint8

const (
	relayMessageText relayMessageType = iota + 1
	relayMessageBinary
	// maxRelayRedirects bounds handshake redirects, matching net/http's
	// default redirect ceiling.
	maxRelayRedirects = 10
)

// relayConn is the complete transport contract required by the relay worker.
// It intentionally mirrors the subset of coder/websocket's API the worker used:
// a context-bound whole-message read and write, a bounded ping that waits for
// the matching pong, a receive limit and an abrupt close.
//
// Concurrency contract: exactly one goroutine calls Read and exactly one calls
// Write; Ping and CloseNow may be called concurrently with both. Read observes
// the connection lifetime, not cancellation of the ctx argument after entry:
// the worker binds a single context.AfterFunc that calls CloseNow when the
// connection's lifetime ends, so no per-frame deadline scaffolding is needed.
type relayConn interface {
	Read(ctx context.Context) (relayMessageType, []byte, error)
	Write(ctx context.Context, typ relayMessageType, data []byte) error
	Ping(ctx context.Context) error
	SetReadLimit(n int64)
	CloseNow() error
}

// resolveWSRedirect resolves a handshake Location header and maps HTTP schemes
// back to their WebSocket equivalents. Relative locations are supported.
func resolveWSRedirect(base *url.URL, location string) (*url.URL, error) {
	ref, err := url.Parse(location)
	if err != nil {
		return nil, errors.New("relay redirect has an invalid location")
	}
	next := base.ResolveReference(ref)
	switch strings.ToLower(next.Scheme) {
	case "http":
		next.Scheme = "ws"
	case "https":
		next.Scheme = "wss"
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("relay redirect uses unsupported scheme %q", next.Scheme)
	}
	if next.Hostname() == "" {
		return nil, errors.New("relay redirect has no host")
	}
	return next, nil
}

func isWSRedirect(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}
