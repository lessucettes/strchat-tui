package client

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSOCKSWSSKeepsCertificateVerification(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			defer conn.CloseNow()
			conn.Write(r.Context(), websocket.MessageText, []byte("verified-tls"))
			conn.Read(r.Context())
		}
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	address, _ := startTestSOCKS(t, server.Listener.Addr().String(), "forward")
	c := proxyTestClient(t, address)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := "wss" + strings.TrimPrefix(server.URL, "https")
	conn, _, err := c.dialRelay(ctx, url)
	if conn != nil {
		conn.CloseNow()
	}
	if err == nil {
		t.Fatal("SOCKS5 silently disabled certificate verification")
	}
	transport := c.relayHTTPClient.Transport.(*http.Transport)
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("test fixture must trust its CA without disabling verification")
	}
	conn, _, err = c.dialRelay(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "verified-tls" {
		t.Fatalf("verified WSS over SOCKS5 failed: %q, %v", data, err)
	}
}

func TestSOCKSRedirectKeepsUsingProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://second.onion/socket", http.StatusFound)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			defer conn.CloseNow()
			conn.Write(r.Context(), websocket.MessageText, []byte("redirected"))
			conn.Read(r.Context())
		}
	}))
	defer server.Close()
	address, requests := startTestSOCKS(t, server.Listener.Addr().String(), "forward")
	c := proxyTestClient(t, address)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := c.dialRelay(ctx, "ws://first.onion/redirect")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for _, host := range []string{"first.onion", "second.onion"} {
		select {
		case request := <-requests:
			if request.host != host || request.atyp != 3 {
				t.Fatalf("redirect changed proxy routing: %+v, want %s", request, host)
			}
		case <-ctx.Done():
			t.Fatal("redirected destination did not reach SOCKS5")
		}
	}
}
