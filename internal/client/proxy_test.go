package client

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type socksRequest struct {
	host string
	port uint16
	atyp byte
}

// startTestSOCKS always forwards to a loopback fixture, never the requested host.
func startTestSOCKS(t *testing.T, target, mode string) (string, <-chan socksRequest) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan socksRequest, 32)
	var mu sync.Mutex
	var connections []net.Conn
	var workers sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, conn)
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				var greeting [2]byte
				if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting[0] != 5 {
					return
				}
				methods := make([]byte, int(greeting[1]))
				if _, err := io.ReadFull(conn, methods); err != nil {
					return
				}
				if mode == "stall" {
					io.Copy(io.Discard, conn)
					return
				}
				if _, err := conn.Write([]byte{5, 0}); err != nil {
					return
				}
				var header [4]byte
				if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 5 || header[1] != 1 {
					return
				}
				var host []byte
				switch header[3] {
				case 1:
					host = make([]byte, 4)
				case 4:
					host = make([]byte, 16)
				case 3:
					var size [1]byte
					if _, err := io.ReadFull(conn, size[:]); err != nil {
						return
					}
					host = make([]byte, int(size[0]))
				default:
					return
				}
				if _, err := io.ReadFull(conn, host); err != nil {
					return
				}
				var port [2]byte
				if _, err := io.ReadFull(conn, port[:]); err != nil {
					return
				}
				name := string(host)
				if header[3] != 3 {
					name = net.IP(host).String()
				}
				requests <- socksRequest{host: name, port: binary.BigEndian.Uint16(port[:]), atyp: header[3]}
				if mode == "reject" {
					conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				upstream, err := net.DialTimeout("tcp", target, time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				conn.SetDeadline(time.Time{})
				copied := make(chan struct{})
				go func() {
					io.Copy(upstream, conn)
					upstream.Close()
					conn.Close()
					close(copied)
				}()
				io.Copy(conn, upstream)
				conn.Close()
				upstream.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-accepted
		mu.Lock()
		for _, conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return listener.Addr().String(), requests
}

func proxyTestClient(t *testing.T, address string) *client {
	t.Helper()
	c, _ := testClient(t)
	httpClient, err := newRelayHTTPClient(address)
	if err != nil {
		t.Fatal(err)
	}
	c.relayHTTPClient = httpClient
	t.Cleanup(httpClient.CloseIdleConnections)
	return c
}

func TestSOCKSRelayUsesProxyForLoopback(t *testing.T) {
	t.Setenv("NO_PROXY", "*")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.Write(r.Context(), websocket.MessageText, []byte("through-socks"))
		conn.Read(r.Context())
	}))
	defer server.Close()
	address, requests := startTestSOCKS(t, server.Listener.Addr().String(), "forward")
	c := proxyTestClient(t, address)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := c.dialRelay(ctx, "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	select {
	case request := <-requests:
		if request.host != "127.0.0.1" {
			t.Fatalf("wrong SOCKS5 destination: %+v", request)
		}
	default:
		t.Fatal("relay connection bypassed the explicit SOCKS5 proxy")
	}
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "through-socks" {
		t.Fatalf("WebSocket frames did not traverse SOCKS5: %q, %v", data, err)
	}
}
