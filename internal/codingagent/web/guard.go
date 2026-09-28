package web

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// errPrivateAddress marks a connection refused by the network guard.
var errPrivateAddress = errors.New("private network address")

// cgnat is the carrier-grade NAT range, private in practice.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// blockedAddr reports whether a dial target is off limits without
// allowPrivateNetwork: loopback, private, link-local, CGNAT, unspecified and
// multicast addresses, including their IPv4-mapped IPv6 forms.
func blockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() || cgnat.Contains(addr)
}

// guardedClient returns the fetch client. Without allowPrivate, every
// connection, including each redirect hop, is checked after DNS resolution
// at dial time, so a public name that resolves to a private address is
// refused too.
func guardedClient(allowPrivate bool, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	// An environment proxy is used only with allowPrivate: the guard checks
	// the address dialed, which behind a proxy is the proxy.
	var proxy func(*http.Request) (*url.URL, error)
	if allowPrivate {
		proxy = http.ProxyFromEnvironment
	} else {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			addr, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			if blockedAddr(addr) {
				return fmt.Errorf("%w %s", errPrivateAddress, addr)
			}
			return nil
		}
	}
	transport := &http.Transport{
		Proxy:                 proxy,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
}
