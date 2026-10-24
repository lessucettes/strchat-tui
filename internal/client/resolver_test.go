package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestTermuxNameservers(t *testing.T) {
	data := []byte(`# Termux resolver configuration
nameserver 192.0.2.53
 nameserver 2001:db8::53 # IPv6
nameserver 192.0.2.54;comment
nameserver dns.invalid
nameserver 192.0.2.1:53
nameserver [2001:db8::1]
nameserver
search example.invalid
; nameserver 192.0.2.99
`)
	want := []string{"192.0.2.53:53", "[2001:db8::53]:53", "192.0.2.54:53"}
	if got := termuxNameservers(data); !slices.Equal(got, want) {
		t.Fatalf("nameservers = %v, want %v", got, want)
	}
}

func TestTermuxResolverFallback(t *testing.T) {
	for _, mode := range []string{"no-prefix", "missing", "unreadable", "empty", "invalid", "valid"} {
		t.Run(mode, func(t *testing.T) {
			prefix := t.TempDir()
			t.Setenv("PREFIX", prefix)
			if mode == "no-prefix" {
				t.Setenv("PREFIX", "")
			} else if mode != "missing" {
				path := filepath.Join(prefix, "etc", "resolv.conf")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if mode == "unreadable" {
					// A directory fails ReadFile even when tests run as root.
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					data := ""
					if mode == "invalid" {
						data = "nameserver dns.invalid\nnameserver 127.0.0.1:53\n"
					} else if mode == "valid" {
						data = "nameserver 127.0.0.1\n"
					}
					if err := os.WriteFile(path, []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			wantActive := runtime.GOOS == "android" && mode == "valid"
			if active := termuxResolver() != nil; active != wantActive {
				t.Fatalf("Termux resolver active=%v on %s, want %v", active, runtime.GOOS, wantActive)
			}
			dialer := newRelayDialer(7*time.Second, termuxResolverFromPrefix(os.Getenv("PREFIX")))
			if dialer.Timeout != 7*time.Second || dialer.KeepAlive != 30*time.Second {
				t.Fatal("resolver setup changed socket timing")
			}
			if mode != "valid" {
				if dialer.Resolver != nil {
					t.Fatal("missing Termux DNS must retain the platform resolver")
				}
				return
			}
			if dialer.Resolver == nil || !dialer.Resolver.PreferGo || dialer.Resolver.Dial == nil {
				t.Fatal("valid Termux DNS was not installed on the relay dialer")
			}
		})
	}
}

func TestTermuxResolverDialRotation(t *testing.T) {
	prefix := t.TempDir()
	t.Setenv("PREFIX", prefix)
	if err := os.MkdirAll(filepath.Join(prefix, "etc"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "etc", "resolv.conf"), []byte("nameserver 192.0.2.53\nnameserver 2001:db8::53\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resolver := termuxResolverFromPrefix(prefix)
	if resolver == nil {
		t.Fatal("valid Termux configuration was ignored")
	}
	// Cancellation exercises server selection without opening a socket.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, network := range []string{"udp", "tcp"} {
		_, err := resolver.Dial(ctx, network, "ignored.invalid:9999")
		var op *net.OpError
		if !errors.As(err, &op) || op.Net != network || op.Addr == nil {
			t.Fatalf("DNS dial did not preserve its network: %v", err)
		}
		// All servers fail synchronously, so the last attempted address exposes
		// both failover and rotation; the system DNS address is never used.
		want := "[2001:db8::53]:53"
		if network == "tcp" {
			want = "192.0.2.53:53"
		}
		if op.Addr.String() != want {
			t.Fatalf("last DNS address = %s, want %s", op.Addr, want)
		}
	}
}
