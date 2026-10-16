package dashboard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

// --edit-trust-local (README "Route editor"): a save that comes from this machine, or from a Tailscale device the operator named with
// --allow-host, is made with the owner's token, which the server holds, instead of one typed into the page. The token never leaves the
// server: the page only learns (GET /api/edit/status "local_trusted") that it need not ask for one.
//
// What is trusted is the TCP peer, never anything the client writes:
//   - a connection from this machine (a loopback address, or the dashboard's own listen address) whose client socket belongs to the same user
//     as the dashboard (the kernel's /proc/net/tcp says whose; Linux only, anywhere else this fails closed). A process of that user can read
//     the token file anyway, so it gains nothing; another local user, and a relay that runs as another user (a headerless forwarder, a
//     tunnel's sshd for someone else), is not trusted;
//   - from another machine, an exact address that is a literal Tailscale address (100.64.0.0/10 or fd7a:115c:a1e0::/48) in the --allow-host
//     list, and only when the connection was accepted on a Tailscale address of the dashboard's own (WireGuard authenticates a tailnet source
//     address, so another device cannot claim it; a listener that is also open on a LAN or a wildcard gives that address no such meaning). A name
//     in the list says how the page may be opened (the Host header), not who is calling, so it is never trusted; nor is a LAN or public address.
//
// On top of the peer address, a trusted request must look like it was made by the dashboard's own page, so that a web page the owner has open in
// another tab cannot save through the owner's browser (CSRF): no proxy headers (a reverse proxy on this machine would make every caller
// loopback), an Origin, if there is one, equal to the Host the request was made to, a Sec-Fetch-Site of same-origin or none, and the guard's
// own demands (a JSON Content-Type, which a cross-site form cannot send and a cross-site fetch must pre-flight, which the guard answers 405).
// A request that sends an Authorization header is judged by that token alone, trusted or not.

var tailnet4 = netip.MustParsePrefix("100.64.0.0/10")
var tailnet6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

// IsTailnetAddr reports whether a is a Tailscale address.
func IsTailnetAddr(a netip.Addr) bool {
	a = a.Unmap()
	return tailnet4.Contains(a) || tailnet6.Contains(a)
}

type localTrust struct {
	label string
	peers map[netip.Addr]bool
	uid   uint32                                     // the user the dashboard runs as: a local caller's socket must be this user's
	owner func(client netip.AddrPort) (uint32, bool) // the user a local TCP client socket belongs to (socketOwner; replaced in tests)
}

// TrustPeers picks, from the dashboard's allowed Host list (AllowedHosts), the entries that can be trusted as callers: the literal Tailscale
// addresses. ignored is the rest (names, LAN and public addresses, loopback names), for the operator to be told.
func TrustPeers(allowed []string) (peers []netip.Addr, ignored []string) {
	for _, h := range allowed {
		a, err := netip.ParseAddr(h)
		switch {
		case err != nil:
			ignored = append(ignored, h)
		case a.IsLoopback():
			// loopback is trusted anyway
		case IsTailnetAddr(a):
			peers = append(peers, a.Unmap())
		default:
			ignored = append(ignored, h)
		}
	}
	return peers, ignored
}

// EnableTrustLocal turns --edit-trust-local on: requests from loopback or from one of peers (Tailscale addresses) that send no token are saved
// as the token label. The label must be in the token file the editor loaded. Call it before the handler serves.
func (e *Editor) EnableTrustLocal(label string, peers []netip.Addr) error {
	if !e.cfg.Tokens.Has(label) {
		return fmt.Errorf("--edit-trust-label %q is not a label in the token file", label)
	}
	t := &localTrust{label: label, peers: map[netip.Addr]bool{}, uid: uint32(os.Getuid()), owner: socketOwner}
	for _, p := range peers {
		if !IsTailnetAddr(p) {
			return errors.New("only Tailscale addresses can be trusted peers")
		}
		t.peers[p.Unmap()] = true
	}
	e.trust = t
	return nil
}

// forwardingHeaders say a proxy is between the caller and the dashboard, so the peer address is the proxy's.
var forwardingHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-Ip", "Forwarded", "Via",
	"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-Headers-Info", "Cf-Connecting-Ip", "True-Client-Ip"}

// trustedRequest reports whether r is made by the owner's own machine or device under --edit-trust-local.
func (e *Editor) trustedRequest(r *http.Request) bool {
	t := e.trust
	if t == nil {
		return false
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	a := netip.AddrPortFrom(ap.Addr().Unmap().WithZone(""), ap.Port())
	// the address the connection was accepted on, as net/http records it
	var onto netip.Addr
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if lp, err := netip.ParseAddrPort(la.String()); err == nil {
			onto = lp.Addr().Unmap().WithZone("")
		}
	}
	if a.Addr().IsLoopback() || (onto.IsValid() && onto == a.Addr()) { // the caller is on this machine
		if uid, ok := t.owner(a); !ok || uid != t.uid {
			return false
		}
	} else if !t.peers[a.Addr()] || !onto.IsValid() || !IsTailnetAddr(onto) {
		// another machine: trusted only by a named Tailscale address, and only when it reached us over Tailscale (the connection was accepted
		// on a Tailscale address of ours), so a listener that is also open on a LAN or a wildcard does not turn the address into a claim
		return false
	}
	for _, k := range forwardingHeaders {
		if len(r.Header.Values(k)) > 0 {
			return false
		}
	}
	if o := r.Header.Values("Origin"); len(o) > 1 || (len(o) == 1 && !sameOrigin(o[0], r.Host)) {
		return false
	}
	if s := r.Header.Values("Sec-Fetch-Site"); len(s) > 1 || (len(s) == 1 && s[0] != "same-origin" && s[0] != "none") {
		return false
	}
	return true
}

// sameOrigin reports whether an Origin header names the host the request was made to.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return false
	}
	return strings.EqualFold(u.Host, host)
}
