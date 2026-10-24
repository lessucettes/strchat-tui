package client

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

func newRelayDialer(timeout time.Duration, resolver *net.Resolver) *net.Dialer {
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Resolver:  resolver,
	}
}

// Pure-Go Android builds cannot use Android's cgo resolver, and Termux's DNS
// configuration is under PREFIX rather than /etc. Other platforms retain their
// resolver even if an unrelated PREFIX contains a resolv.conf file.
func termuxResolver() *net.Resolver {
	if runtime.GOOS != "android" {
		return nil
	}
	return termuxResolverFromPrefix(os.Getenv("PREFIX"))
}

// A nil resolver preserves the platform default when Termux DNS is unavailable.
func termuxResolverFromPrefix(prefix string) *net.Resolver {
	if prefix == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(prefix, "etc", "resolv.conf"))
	if err != nil {
		return nil
	}
	servers := termuxNameservers(data)
	if len(servers) == 0 {
		return nil
	}

	var next atomic.Uint32
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			start := int((next.Add(1) - 1) % uint32(len(servers)))
			var lastErr error
			for i := range servers {
				conn, err := dialer.DialContext(ctx, network, servers[(start+i)%len(servers)])
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

func termuxNameservers(data []byte) []string {
	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		if comment := strings.IndexAny(line, "#;"); comment >= 0 {
			line = line[:comment]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		if ip := net.ParseIP(fields[1]); ip != nil {
			servers = append(servers, net.JoinHostPort(ip.String(), "53"))
		}
	}
	return servers
}
