package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/getbooky/booky/internal/release"
)

// This file is what stands between Booky and the open internet: who a
// request really comes from when a reverse proxy sits in front, the headers
// that tell browsers what the pages may do, and a same-origin rule for
// anything that changes state.

// trustedProxies are the peers whose X-Forwarded-For is believed. By default
// that's loopback and the private ranges — where a reverse proxy on the same
// host or Docker network connects from — so a client connecting straight
// from the internet can't claim to be someone else. BOOKY_TRUSTED_PROXIES
// overrides it: a comma-separated list of addresses or CIDR ranges, or
// "none" to believe nobody.
type trustedProxies []netip.Prefix

var defaultTrustedProxies = []string{
	"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
}

func loadTrustedProxies() trustedProxies {
	raw := strings.TrimSpace(os.Getenv("BOOKY_TRUSTED_PROXIES"))
	list := defaultTrustedProxies
	switch {
	case strings.EqualFold(raw, "none"):
		return nil
	case raw != "":
		list = strings.Split(raw, ",")
	}
	var out trustedProxies
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if p, err := netip.ParsePrefix(item); err == nil {
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(item); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		log.Printf("BOOKY_TRUSTED_PROXIES: ignoring %q (not an address or CIDR range)", item)
	}
	return out
}

func (t trustedProxies) has(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range t {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP is the address a request really came from. With no trusted proxy
// in the way it's the peer. Behind one, it's the right-most X-Forwarded-For
// entry that isn't itself a trusted proxy: entries to the left of that were
// written by the client and can say anything.
func (t trustedProxies) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !t.has(peer) {
		return host
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // garbage: stop believing the header here
		}
		if !t.has(a) {
			return a.Unmap().String()
		}
	}
	return host
}

// viaTrustedProxy reports whether the request's peer is a trusted proxy,
// i.e. whether its X-Forwarded-* headers mean anything.
func (t trustedProxies) viaTrustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	return err == nil && t.has(peer)
}

// contentSecurityPolicy is what Booky's pages may load and run. The line
// that matters most is script-src 'self': scripts only from Booky's own
// files — no inline script, no eval, no blob. The web reader shows each
// chapter of a book as a blob: document in a frame, and such documents
// take on this policy, so a script planted in an EPUB can't run and use
// the reader's session. (The frame's sandbox can't do it: it needs
// allow-same-origin and allow-scripts both, which together are no sandbox.)
//
// The rest is what the app needs: inline and blob styles (books carry
// their own, and the UI library injects some); images from Booky, from
// books (data:, blob:) and, for search results, provider covers over
// https; audio from Booky and from books; frames only of book chapters.
const contentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' blob:; " +
	"img-src 'self' data: blob: https:; " +
	"font-src 'self' data: blob:; " +
	"media-src 'self' blob:; " +
	"connect-src 'self' blob:; " +
	"frame-src 'self' blob:; " +
	"worker-src 'none'; " +
	"manifest-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'self'"

// secure wraps the whole HTTP surface.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// everything downstream (login lockouts, OPDS, logs) sees the real
		// client address
		proxied := s.proxies.viaTrustedProxy(r)
		if ip := s.proxies.clientIP(r); ip != "" {
			r.RemoteAddr = net.JoinHostPort(ip, "0")
		}

		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		if r.TLS != nil || (proxied && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		if err := sameOrigin(r, proxied); err != nil {
			writeErr(w, http.StatusForbidden, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses state-changing API requests a browser sends from
// another site. The session cookie is SameSite=Strict, which already stops
// other sites — but "site" means the registrable domain, so another app on
// a sibling subdomain (or a page on it someone managed to plant) would
// still ride the cookie. Browsers say where a request comes from (Origin,
// Sec-Fetch-Site); when they do, it has to be Booky's own origin. Clients
// that send neither — the KOReader plugin, curl, reader apps — carry no
// cookie and are let through to their own credential checks.
func sameOrigin(r *http.Request, proxied bool) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/api/koreader/") {
		return nil
	}
	refuse := errors.New("this request came from another site")
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return refuse
		}
		if strings.EqualFold(u.Host, r.Host) {
			return nil
		}
		// a proxy that rewrites Host passes the original along
		if fwd := r.Header.Get("X-Forwarded-Host"); proxied && fwd != "" && strings.EqualFold(u.Host, strings.TrimSpace(strings.Split(fwd, ",")[0])) {
			return nil
		}
		return refuse
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return nil
	}
	return refuse
}

// truncate cuts s to at most n bytes, for log lines.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Search results leave the server sealed. A release's download URL is not
// the client's business: Prowlarr's carry its API key and internal address,
// and whatever URL comes back in a grab is handed to the download client or
// fetched by Booky itself. So the URL goes out encrypted, bound to the book
// and kind it was found for, and a grab is only accepted when it opens —
// i.e. for a release this server offered for that book. The key lives for
// the life of the process: a restart makes open result lists stale, and
// searching again fixes that.
var sealer = func() cipher.AEAD {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}()

const sealedPrefix = "sealed:"

// sealContext is what a sealed URL is bound to: change any of it and the
// seal doesn't open. Fields are length-prefixed so no two contexts collide.
func sealContext(bookID int64, kind string, r release.Release) []byte {
	var b strings.Builder
	for _, f := range []string{fmt.Sprint(bookID), kind, r.Protocol, r.Source, r.Title} {
		fmt.Fprintf(&b, "%d:%s|", len(f), f)
	}
	return []byte(b.String())
}

func sealURL(bookID int64, kind string, r release.Release) string {
	nonce := make([]byte, sealer.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	ct := sealer.Seal(nonce, nonce, []byte(r.DownloadURL), sealContext(bookID, kind, r))
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(ct)
}

func openURL(bookID int64, kind string, r release.Release) (string, bool) {
	raw, ok := strings.CutPrefix(r.DownloadURL, sealedPrefix)
	if !ok {
		return "", false
	}
	ct, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(ct) < sealer.NonceSize() {
		return "", false
	}
	pt, err := sealer.Open(nil, ct[:sealer.NonceSize()], ct[sealer.NonceSize():], sealContext(bookID, kind, r))
	if err != nil {
		return "", false
	}
	return string(pt), true
}

// signReleases seals search results on their way out: each release, and
// each part of a set.
func (s *Server) signReleases(bookID int64, kind string, rels []release.Release) {
	for i := range rels {
		rels[i].DownloadURL = sealURL(bookID, kind, rels[i])
		rels[i].InfoURL = "" // indexer pages: nothing the UI shows
		for j := range rels[i].Parts {
			rels[i].Parts[j].DownloadURL = sealURL(bookID, kind, rels[i].Parts[j])
			rels[i].Parts[j].InfoURL = ""
		}
	}
}

// sealedRelease opens a grab's release in place, answering the request
// itself when it isn't one this server offered for this book.
func (s *Server) sealedRelease(w http.ResponseWriter, bookID int64, kind string, r *release.Release) bool {
	u, ok := openURL(bookID, kind, *r)
	if ok {
		r.DownloadURL = u
		for i := range r.Parts {
			pu, pok := openURL(bookID, kind, r.Parts[i])
			if !pok {
				ok = false
				break
			}
			r.Parts[i].DownloadURL = pu
		}
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("that result is out of date — search again, then grab"))
	}
	return ok
}

// sameURL compares two service addresses as a person would: case and a
// trailing slash don't make a different server.
func sameURL(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	return norm(a) == norm(b)
}
