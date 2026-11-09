// Package safenet makes outbound connections that can only reach the public
// internet. Booky fetches things at the say-so of people and services it
// doesn't fully trust — a cover URL in a search result, a mail server a
// user typed in — and from where it runs it can usually reach a good deal
// that they can't: the download client's API, a router's admin page, a
// cloud metadata endpoint. Connections made through this package are
// refused when the address they actually resolve to is loopback, private,
// link-local or otherwise not a public unicast address. The check runs at
// connect time, on the resolved address, so neither a redirect nor a DNS
// name that points inward gets round it.
package safenet

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrPrivate is returned when a connection would land somewhere private.
var ErrPrivate = errors.New("that address isn't on the public internet")

var blocked = func() []netip.Prefix {
	var out []netip.Prefix
	for _, c := range []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved
		"64:ff9b::/96",    // NAT64: maps onto IPv4, private ranges included
		"2001:db8::/32",   // documentation
	} {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}()

// Public reports whether an address is an ordinary public one.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// control vets the address a socket is about to connect to.
func control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w (%s)", ErrPrivate, address)
	}
	if !Public(ap.Addr()) {
		return ErrPrivate
	}
	return nil
}

// Dialer returns a dialer that only connects to public addresses.
func Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: control}
}

// Client returns an HTTP client that only talks to public addresses, never
// goes through a proxy from the environment, and gives up on long chains
// of redirects.
func Client(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           Dialer(10 * time.Second).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to a non-web address")
			}
			return nil
		},
	}
}

// Scrub takes the URL out of an HTTP client error. Go's client errors
// quote the whole request URL, and several services Booky talks to carry
// their API key in it; these errors end up in queue rows, history and
// error replies, where the key has no business being. The error still
// unwraps to the original, so errors.Is and errors.As keep working.
func Scrub(err error) error {
	var uerr *url.Error
	if err == nil || !errors.As(err, &uerr) {
		return err
	}
	clean := uerr.Op + ": " + uerr.Err.Error()
	return scrubbed{msg: strings.Replace(err.Error(), uerr.Error(), clean, 1), err: err}
}

type scrubbed struct {
	msg string
	err error
}

func (s scrubbed) Error() string { return s.msg }
func (s scrubbed) Unwrap() error { return s.err }
