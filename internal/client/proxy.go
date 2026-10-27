package client

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Options contains process-local settings, never saved to config.json.
type Options struct {
	SOCKSProxy    string
	PluginSend    string
	PluginReceive string
	PluginTimeout time.Duration // zero uses the default execution timeout
}

// Tor may need to fetch onion descriptors and build a circuit on the first dial.
const socksConnectTimeout = time.Minute

func newRelayHTTPClient(address string) (*http.Client, error) {
	return newRelayHTTPClientWithResolver(address, termuxResolver())
}

func newRelayHTTPClientWithResolver(address string, resolver *net.Resolver) (*http.Client, error) {
	if address == "" {
		return nil, nil
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, errors.New("SOCKS5 proxy address cannot be blank")
	}
	if !strings.Contains(address, "://") {
		address = "socks5://" + address
	}
	proxy, err := url.Parse(address)
	if err != nil {
		return nil, errors.New("invalid SOCKS5 proxy address")
	}
	proxy.Scheme = strings.ToLower(proxy.Scheme)
	if proxy.Scheme != "socks5" && proxy.Scheme != "socks5h" {
		return nil, errors.New("SOCKS5 proxy must use host:port, socks5://host:port or socks5h://host:port")
	}
	if proxy.User != nil || proxy.Hostname() == "" || proxy.Path != "" || proxy.RawQuery != "" || proxy.ForceQuery || strings.Contains(address, "#") {
		return nil, errors.New("SOCKS5 proxy requires a host and port without credentials, path, query or fragment")
	}
	_, port, err := net.SplitHostPort(proxy.Host)
	if err != nil {
		return nil, errors.New("SOCKS5 proxy requires an explicit host:port")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return nil, errors.New("SOCKS5 proxy port must be in the range 1..65535")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if dialer := newRelayDialer(30*time.Second, resolver); dialer.Resolver != nil {
		transport.DialContext = dialer.DialContext
	}
	// Go's SOCKS5 transport forwards destination names to the proxy. An explicit
	// ProxyURL also overrides environment proxies and their loopback bypasses.
	transport.Proxy = http.ProxyURL(proxy)
	return &http.Client{Transport: transport}, nil
}
