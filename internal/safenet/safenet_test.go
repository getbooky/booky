package safenet

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":              true,
		"2606:4700:4700::1111": true,
		"127.0.0.1":            false,
		"::1":                  false,
		"10.1.2.3":             false,
		"172.17.0.1":           false, // the Docker bridge
		"192.168.1.10":         false,
		"169.254.169.254":      false, // cloud metadata
		"100.100.100.200":      false, // carrier-grade NAT / another cloud's metadata
		"0.0.0.0":              false,
		"::ffff:127.0.0.1":     false, // IPv4-mapped loopback
		"::ffff:192.168.1.1":   false,
		"fe80::1":              false,
		"fd00::1":              false,
		"64:ff9b::7f00:1":      false, // NAT64 of 127.0.0.1
		"224.0.0.1":            false,
		"255.255.255.255":      false,
	} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Public(%s) = %v, want %v", addr, got, want)
		}
	}
}

// A server on this machine stands in for "something on the inside": the
// client must refuse to reach it, directly or through a redirect.
func TestClientRefusesTheInside(t *testing.T) {
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the inside server was reached")
	}))
	defer inside.Close()
	c := Client(5 * time.Second)
	res, err := c.Get(inside.URL)
	if err == nil {
		res.Body.Close()
		t.Fatal("fetched a loopback address")
	}
	if !errors.Is(err, ErrPrivate) {
		t.Errorf("err = %v, want ErrPrivate", err)
	}
	// "localhost" resolves inward too
	if res, err := c.Get("http://localhost:1/"); err == nil {
		res.Body.Close()
		t.Fatal("fetched localhost")
	} else if !errors.Is(err, ErrPrivate) {
		t.Errorf("localhost: err = %v, want ErrPrivate", err)
	}
}
