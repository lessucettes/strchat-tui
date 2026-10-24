package client

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testRelayResolver answers only the specified synthetic name over loopback.
// Unknown names fail locally, including a relay name accidentally leaked from
// a proxy path. No system resolver or public DNS server is used.
func testRelayResolver(t *testing.T, allowed string) (*net.Resolver, *atomic.Int32) {
	t.Helper()
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var queries atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, peer, err := server.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			end := 12
			var labels []string
			for end < n && buf[end] != 0 {
				size := int(buf[end])
				end++
				if end+size >= n {
					break
				}
				labels = append(labels, string(buf[end:end+size]))
				end += size
			}
			end++ // root label
			if end+4 > n {
				continue
			}
			qtype := binary.BigEndian.Uint16(buf[end:])
			end += 4 // question type and class
			reply := make([]byte, 12, 512)
			copy(reply[:2], buf[:2])
			binary.BigEndian.PutUint16(reply[2:], 0x8180) // response, recursion available
			binary.BigEndian.PutUint16(reply[4:], 1)
			reply = append(reply, buf[12:end]...)
			if host := strings.Join(labels, "."); host != allowed {
				t.Errorf("unexpected local DNS query: %q, want only %q", host, allowed)
				reply[3] = 0x83 // NXDOMAIN
			} else {
				queries.Add(1)
				if qtype == 1 { // A: other types have a successful empty answer.
					binary.BigEndian.PutUint16(reply[6:], 1)
					reply = append(reply, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 127, 0, 0, 1)
				}
			}
			_, _ = server.WriteTo(reply, peer)
		}
	}()
	t.Cleanup(func() {
		server.Close()
		<-done
	})
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.LocalAddr().String())
	}}, &queries
}

func TestRelayResolverPropagation(t *testing.T) {
	// A wiring regression must fail locally rather than contact system DNS.
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("relay bypassed the supplied resolver")
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
	for _, mode := range []string{"default", "nil-transport", "direct-transport", "socks5", "socks5h", "http-proxy"} {
		t.Run(mode, func(t *testing.T) {
			target, _ := wsTestEchoServer(t, false)
			targetAddr := target.Listener.Addr().String()
			_, targetPort, _ := net.SplitHostPort(targetAddr)
			allowed := "relay.invalid"
			targetURL := "ws://relay.invalid:" + targetPort
			var httpClient *http.Client
			var requests <-chan socksRequest
			var proxyURL *url.URL
			switch mode {
			case "nil-transport":
				httpClient = &http.Client{}
			case "direct-transport":
				httpClient = &http.Client{Transport: &http.Transport{}}
			case "socks5", "socks5h":
				allowed = "proxy.invalid"
				address, seen := startTestSOCKS(t, targetAddr, "forward")
				requests = seen
				_, port, _ := net.SplitHostPort(address)
				proxyURL, _ = url.Parse(mode + "://proxy.invalid:" + port)
				targetURL = "ws://relay-test.onion/socket"
			case "http-proxy":
				allowed = "proxy.invalid"
				proxy, _ := wsTestCONNECTProxy(t, false, "")
				proxyURL, _ = url.Parse(proxy.URL)
				proxyURL.Host = net.JoinHostPort(allowed, proxyURL.Port())
				// CONNECT retains the destination; only the proxy needs DNS.
				targetURL = "ws://" + targetAddr
			}
			resolver, queries := testRelayResolver(t, allowed)
			if mode == "socks5" || mode == "socks5h" {
				var err error
				httpClient, err = newRelayHTTPClientWithResolver(proxyURL.String(), resolver)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(httpClient.CloseIdleConnections)
			} else if proxyURL != nil {
				httpClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
			}
			settings, err := relayDialSettingsWithResolver(httpClient, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "default" || mode == "nil-transport" {
				// Avoid process-cached environment proxy settings in this DNS test.
				settings.proxy = nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			parsed, _ := url.Parse(targetURL)
			conn, _, err := dialGorilla(ctx, parsed, settings, time.Second)
			if err != nil {
				t.Fatalf("relay dial with the supplied resolver: %v", err)
			}
			defer conn.Close()
			_, data, err := conn.ReadMessage()
			if err != nil || string(data) != "through-proxy" {
				t.Fatalf("relay traffic failed: %q, %v", data, err)
			}
			if queries.Load() == 0 {
				t.Fatal("dial bypassed the supplied resolver")
			}
			if requests != nil {
				select {
				case request := <-requests:
					if request.atyp != 3 || request.host != "relay-test.onion" || request.port != 80 {
						t.Fatalf("SOCKS destination changed or was resolved locally: %+v", request)
					}
				default:
					t.Fatal("SOCKS proxy was bypassed")
				}
			}
		})
	}
}

func TestRelayResolverPreservesCustomDialContext(t *testing.T) {
	for _, proxy := range []string{"", "socks5://proxy.invalid:1080", "http://proxy.invalid:8080"} {
		t.Run(proxy, func(t *testing.T) {
			resolver := &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
				t.Error("custom DialContext must retain ownership of DNS")
				return nil, errors.New("unexpected DNS")
			}}
			var addresses []string
			stop := errors.New("custom dial stopped")
			transport := &http.Transport{DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
				addresses = append(addresses, address)
				return nil, stop
			}}
			want := "relay.invalid:80"
			if proxy != "" {
				proxyURL, _ := url.Parse(proxy)
				transport.Proxy = http.ProxyURL(proxyURL)
				want = proxyURL.Host
			}
			settings, err := relayDialSettingsWithResolver(&http.Client{Transport: transport}, resolver)
			if err != nil {
				t.Fatal(err)
			}
			target, _ := url.Parse("ws://relay.invalid")
			_, err = dialRelayNet(context.Background(), target, settings)
			if !errors.Is(err, stop) || !slices.Equal(addresses, []string{want}) {
				t.Fatalf("custom dial contract changed: addresses=%v err=%v", addresses, err)
			}
		})
	}
}
