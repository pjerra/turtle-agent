package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The --edit-trust-local path (trust.go): who is let save without a token, and that the token is never sent out.

const (
	trustSelf  = "100.86.248.75" // the dashboard's own tailnet address (the --listen address)
	trustPeer  = "100.101.102.103"
	trustOther = "100.99.99.99" // a tailnet device that is not named
)

type trustRig struct {
	*editRig
	h http.Handler
	// the user a local caller's socket belongs to, and whether the kernel finds it (the real lookup is tested on its own)
	uid   uint32
	found bool
}

func newTrustRig(t *testing.T, on bool) *trustRig {
	t.Helper()
	r := newEditRig(t, nil)
	if on {
		peers, _ := TrustPeers([]string{trustSelf, trustPeer, "laptop.tail1234.ts.net", "192.168.1.5", "127.0.0.1"})
		must(t, r.ed.EnableTrustLocal("owner", peers))
	}
	allow := []string{trustSelf, trustPeer, trustOther, "laptop.tail1234.ts.net", "192.168.1.5", "203.0.113.9"}
	tr := &trustRig{editRig: r, uid: uint32(os.Getuid()), found: true}
	if on {
		r.ed.trust.owner = func(netip.AddrPort) (uint32, bool) { return tr.uid, tr.found }
	}
	tr.h = HandlerWithEditor(r.p, "", allow, nil, r.ed)
	return tr
}

// as sends a request as if its TCP peer were remote, to Host host, with extra headers; the body is a valid edit of the test plan.
func (tr *trustRig) as(method, path, remote, host string, hdr map[string]string) *httptest.ResponseRecorder {
	return tr.asOn(method, path, remote, host, "", hdr)
}

// asOn is as with the address the connection was accepted on given (local, "" = the Host when that is an address). The page's nonce is sent unless
// hdr names the header with an empty value (left out) or another one.
func (tr *trustRig) asOn(method, path, remote, host, local string, hdr map[string]string) *httptest.ResponseRecorder {
	var body []byte
	if method == http.MethodPost {
		body, _ = json.Marshal(tr.edit(move(0, 0, 21, 1)))
	}
	req := httptest.NewRequest(method, "http://"+host+path, bytes.NewReader(body))
	req.RemoteAddr = remote
	if local == "" {
		local, _, _ = net.SplitHostPort(host)
	}
	if ip := net.ParseIP(strings.Trim(local, "[]")); ip != nil { // the address the dashboard was reached on, as net/http records it
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: ip, Port: 8790}))
	}
	if tr.ed.trust != nil {
		req.Header.Set(nonceHeader, tr.ed.trust.nonce)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	tr.h.ServeHTTP(rec, req)
	return rec
}

func TestTrustLocalAcceptsTheLocalHostAndTheNamedTailnetPeers(t *testing.T) {
	tr := newTrustRig(t, true)
	for name, c := range map[string]struct{ remote, host string }{
		"loopback v4":                 {"127.0.0.1:50000", "127.0.0.1:8790"},
		"loopback v6":                 {"[::1]:50000", "[::1]:8790"},
		"loopback mapped":             {"[::ffff:127.0.0.1]:50000", "127.0.0.1:8790"},
		"the dashboard's own tailnet": {trustSelf + ":50000", trustSelf + ":8790"},
		"a named tailnet peer":        {trustPeer + ":50000", trustSelf + ":8790"},
	} {
		rec := tr.as(http.MethodPost, "/api/edit/edit1", c.remote, c.host, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
			continue
		}
		var rep EditReply
		must(t, json.Unmarshal(rec.Body.Bytes(), &rep))
		if rep.Who != "owner" || rep.Version < 1 {
			t.Errorf("%s: %+v", name, rep)
		}
	}
}

func TestTrustLocalRejectsEveryoneElse(t *testing.T) {
	tr := newTrustRig(t, true)
	before := tree(t, tr.edits)
	for name, remote := range map[string]string{
		"a tailnet device that is not named": trustOther + ":50000",
		"a LAN address in the list":          "192.168.1.5:50000",
		"a public address in the list":       "203.0.113.9:50000",
		"an address nobody named":            "198.51.100.7:50000",
		"a name in the list is no caller":    "10.0.0.1:50000",
		"no port":                            "garbage",
	} {
		rec := tr.as(http.MethodPost, "/api/edit/edit1", remote, trustSelf+":8790", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// a named peer that reaches the dashboard other than over Tailscale (a LAN or public address of the machine, or no recorded local address) is
	// not a Tailscale peer
	for name, host := range map[string]string{"a LAN address": "192.168.1.5:8790", "a public address": "203.0.113.9:8790"} {
		if rec := tr.as(http.MethodPost, "/api/edit/edit1", trustPeer+":50000", host, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("peer reaching %s: %d %s", name, rec.Code, rec.Body)
		}
	}
	b, _ := json.Marshal(tr.edit(move(0, 0, 21, 1)))
	req := httptest.NewRequest(http.MethodPost, "http://"+trustSelf+":8790/api/edit/edit1", bytes.NewReader(b))
	req.RemoteAddr = trustPeer + ":50000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	tr.h.ServeHTTP(rec, req) // no local address in the context
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("peer with no recorded local address: %d", rec.Code)
	}
	if tree(t, tr.edits) != before {
		t.Error("a refused request wrote")
	}
}

// A caller on this machine is trusted only when its socket belongs to the dashboard's own user. A relay that runs as someone else, or a socket the
// kernel does not show, is not; a tailnet peer on another machine has no local socket to check.
func TestTrustLocalNeedsTheSameUserOnThisMachine(t *testing.T) {
	for name, c := range map[string]struct {
		remote, host string
		uid          uint32
		found        bool
		want         int
	}{
		"loopback, same user":                   {"127.0.0.1:50000", "127.0.0.1:8790", uint32(os.Getuid()), true, http.StatusOK},
		"loopback, another user":                {"127.0.0.1:50000", "127.0.0.1:8790", uint32(os.Getuid()) + 1, true, http.StatusUnauthorized},
		"loopback, socket not found":            {"127.0.0.1:50000", "127.0.0.1:8790", uint32(os.Getuid()), false, http.StatusUnauthorized},
		"own tailnet address, same user":        {trustSelf + ":50000", trustSelf + ":8790", uint32(os.Getuid()), true, http.StatusOK},
		"own tailnet address, another user":     {trustSelf + ":50000", trustSelf + ":8790", uint32(os.Getuid()) + 1, true, http.StatusUnauthorized},
		"own tailnet address, socket not found": {trustSelf + ":50000", trustSelf + ":8790", 0, false, http.StatusUnauthorized},
		"a named peer needs no local socket":    {trustPeer + ":50000", trustSelf + ":8790", 0, false, http.StatusOK},
		"a named peer is not asked whose it is": {trustPeer + ":50000", trustSelf + ":8790", uint32(os.Getuid()) + 1, true, http.StatusOK},
	} {
		tr := newTrustRig(t, true)
		tr.uid, tr.found = c.uid, c.found
		rec := tr.as(http.MethodPost, "/api/edit/edit1", c.remote, c.host, nil)
		if rec.Code != c.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// The real lookup, on a real connection: the kernel names this process's user as the owner of its own client socket, and the editor takes the
// save; the same request from a stranger's address does not.
func TestTrustLocalOnARealConnection(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("no /proc/net/tcp")
	}
	r := newEditRig(t, nil)
	must(t, r.ed.EnableTrustLocal("owner", nil))
	srv := httptest.NewServer(HandlerWithEditor(r.p, "", nil, nil, r.ed))
	defer srv.Close()
	b, _ := json.Marshal(r.edit(move(0, 0, 21, 1)))
	post := func(nonce string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/edit/edit1", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if nonce != "" {
			req.Header.Set(nonceHeader, nonce)
		}
		resp, err := http.DefaultClient.Do(req)
		must(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	// the nonce comes from the status answer, which is only given to a trusted caller
	resp, err := http.Get(srv.URL + "/api/edit/status")
	must(t, err)
	var st struct {
		Nonce string `json:"trust_nonce"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&st))
	resp.Body.Close()
	if st.Nonce == "" {
		t.Fatal("no nonce for a local caller")
	}
	if c := post(""); c != http.StatusUnauthorized {
		t.Errorf("no nonce: %d", c)
	}
	if c := post(st.Nonce); c != http.StatusOK {
		t.Fatalf("a save from this process over loopback: %d", c)
	}
	// and a socket that is not in the kernel's table (a made-up port) has no owner
	if _, ok := socketOwner(netip.MustParseAddrPort("127.0.0.1:1")); ok {
		t.Error("an owner was found for a socket that is not there")
	}
}

func TestParseProcAddr(t *testing.T) {
	for in, want := range map[string]string{
		"0100007F:1F96":                         "127.0.0.1:8086",
		"00000000000000000000000001000000:0050": "[::1]:80",
		"0000000000000000FFFF00000100007F:0050": "127.0.0.1:80",
	} {
		got, ok := parseProcAddr(in)
		if !ok || got.String() != want {
			t.Errorf("%s: %v %v, want %s", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "zz:1", "0100007F", "0100007F:zz", "0100:1"} {
		if _, ok := parseProcAddr(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

// DNS rebinding: a name that is allowed (--allow-host) and that an attacker controls can be rebound to 127.0.0.1 or to the machine's own address.
// The page it serves is then same-origin with a socket of the owner's user: Host, Origin and Sec-Fetch-Site all agree. A name never makes a caller
// local, so none of these is trusted.
func TestTrustLocalIgnoresAHostNameOnThisMachine(t *testing.T) {
	const rebind = "laptop.tail1234.ts.net:8790" // allowed by name, as the owner may have listed it
	same := map[string]string{"Origin": "http://" + rebind, "Sec-Fetch-Site": "same-origin"}
	for name, c := range map[string]struct{ remote, local string }{
		"rebound to loopback":                  {"127.0.0.1:50000", "127.0.0.1"},
		"rebound to loopback v6":               {"[::1]:50000", "::1"},
		"rebound to the machine's own address": {trustSelf + ":50000", trustSelf},
	} {
		tr := newTrustRig(t, true)
		before := tree(t, tr.edits)
		if rec := tr.asOn(http.MethodPost, "/api/edit/edit1", c.remote, rebind, c.local, same); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		st := tr.asOn(http.MethodGet, "/api/edit/status", c.remote, rebind, c.local, nil)
		if !strings.Contains(st.Body.String(), `"local_trusted":false`) || strings.Contains(st.Body.String(), tr.ed.trust.nonce) {
			t.Errorf("%s: status %s", name, st.Body)
		}
		if tree(t, tr.edits) != before {
			t.Errorf("%s wrote", name)
		}
	}
	// a literal address that is not this connection's own address is no better
	tr := newTrustRig(t, true)
	if rec := tr.asOn(http.MethodPost, "/api/edit/edit1", "127.0.0.1:50000", "192.168.1.5:8790", "127.0.0.1", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a literal LAN Host on a loopback connection: %d", rec.Code)
	}
}

// The remote branch is by the peer's Tailscale address and works for a Host that is a name (the owner opens the page by its Tailscale name).
func TestTrustLocalAPeerByTheTailscaleName(t *testing.T) {
	tr := newTrustRig(t, true)
	const host = "laptop.tail1234.ts.net:8790"
	st := tr.asOn(http.MethodGet, "/api/edit/status", trustPeer+":50000", host, trustSelf, nil)
	var got struct {
		Trusted bool   `json:"local_trusted"`
		Nonce   string `json:"trust_nonce"`
	}
	must(t, json.Unmarshal(st.Body.Bytes(), &got))
	if !got.Trusted || got.Nonce == "" || got.Nonce != tr.ed.trust.nonce {
		t.Fatalf("status: %s", st.Body)
	}
	hdr := map[string]string{nonceHeader: got.Nonce, "Origin": "http://" + host, "Sec-Fetch-Site": "same-origin"}
	if rec := tr.asOn(http.MethodPost, "/api/edit/edit1", trustPeer+":50000", host, trustSelf, hdr); rec.Code != http.StatusOK {
		t.Errorf("a save with the nonce: %d %s", rec.Code, rec.Body)
	}
}

// A trusted save must echo the page's nonce. The real token needs none.
func TestTrustLocalSaveNeedsThePageNonce(t *testing.T) {
	for name, hdr := range map[string]map[string]string{
		"no nonce":        {nonceHeader: ""},
		"a wrong nonce":   {nonceHeader: strings.Repeat("0", 64)},
		"a truncated one": {nonceHeader: "abc"},
	} {
		for who, remote := range map[string]string{"local": "127.0.0.1:50000", "a tailnet peer": trustPeer + ":50000"} {
			tr := newTrustRig(t, true)
			host, local := "127.0.0.1:8790", ""
			if who != "local" {
				host, local = trustSelf+":8790", trustSelf
			}
			before := tree(t, tr.edits)
			if rec := tr.asOn(http.MethodPost, "/api/edit/edit1", remote, host, local, hdr); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s, %s: %d %s", name, who, rec.Code, rec.Body)
			}
			if tree(t, tr.edits) != before {
				t.Errorf("%s, %s wrote", name, who)
			}
		}
	}
	// two nonce headers are none
	tr := newTrustRig(t, true)
	b, _ := json.Marshal(tr.edit(move(0, 0, 21, 1)))
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8790/api/edit/edit1", bytes.NewReader(b))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add(nonceHeader, tr.ed.trust.nonce)
	req.Header.Add(nonceHeader, tr.ed.trust.nonce)
	rec := httptest.NewRecorder()
	tr.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("two nonces: %d", rec.Code)
	}
	// the real token needs no nonce, and an untrusted caller is never given one
	if rec := tr.asOn(http.MethodPost, "/api/edit/edit1", "198.51.100.7:1", "127.0.0.1:8790", "", map[string]string{nonceHeader: "", "Authorization": "Bearer " + editToken}); rec.Code != http.StatusOK {
		t.Errorf("token without nonce: %d %s", rec.Code, rec.Body)
	}
	if st := tr.asOn(http.MethodGet, "/api/edit/status", "198.51.100.7:1", "127.0.0.1:8790", "", nil); strings.Contains(st.Body.String(), "trust_nonce") {
		t.Errorf("a stranger was given the nonce: %s", st.Body)
	}
	// the nonce is not derived from a token
	for _, tok := range []string{editToken, strings.Repeat("h", 40)} {
		if strings.Contains(tr.ed.trust.nonce, tok) || len(tr.ed.trust.nonce) != 64 {
			t.Error("nonce shape")
		}
	}
}

func TestTrustLocalNeedsTheFlag(t *testing.T) {
	tr := newTrustRig(t, false)
	if rec := tr.as(http.MethodPost, "/api/edit/edit1", "127.0.0.1:50000", "127.0.0.1:8790", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("flag off, loopback: %d", rec.Code)
	}
	rec := tr.as(http.MethodGet, "/api/edit/status", "127.0.0.1:50000", "127.0.0.1:8790", nil)
	if !strings.Contains(rec.Body.String(), `"local_trusted":false`) {
		t.Errorf("status: %s", rec.Body)
	}
}

func TestTrustLocalIsNotFooledByTheRequest(t *testing.T) {
	local := "127.0.0.1:50000"
	for name, c := range map[string]struct {
		host string
		hdr  map[string]string
	}{
		"a proxy header (X-Forwarded-For)": {"127.0.0.1:8790", map[string]string{"X-Forwarded-For": "198.51.100.7"}},
		"Forwarded":                        {"127.0.0.1:8790", map[string]string{"Forwarded": "for=198.51.100.7"}},
		"X-Real-IP":                        {"127.0.0.1:8790", map[string]string{"X-Real-Ip": "198.51.100.7"}},
		"tailscale serve":                  {"127.0.0.1:8790", map[string]string{"Tailscale-User-Login": "x@example.com"}},
		"a cross-site Origin":              {"127.0.0.1:8790", map[string]string{"Origin": "https://evil.example"}},
		"a null Origin":                    {"127.0.0.1:8790", map[string]string{"Origin": "null"}},
		"an Origin of another port":        {"127.0.0.1:8790", map[string]string{"Origin": "http://127.0.0.1:9999"}},
		"Sec-Fetch-Site cross-site":        {"127.0.0.1:8790", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		"Sec-Fetch-Site same-site":         {"127.0.0.1:8790", map[string]string{"Sec-Fetch-Site": "same-site"}},
		"a Host the guard does not know":   {"evil.example.com", nil},
		"a wrong token is not forgiven":    {"127.0.0.1:8790", map[string]string{"Authorization": "Bearer " + strings.Repeat("z", 40)}},
		"a Basic header is not forgiven":   {"127.0.0.1:8790", map[string]string{"Authorization": "Basic abc"}},
	} {
		tr := newTrustRig(t, true) // a rig each: the failed-token limit would answer 429 after ten
		rec := tr.as(http.MethodPost, "/api/edit/edit1", local, c.host, c.hdr)
		want := http.StatusUnauthorized
		if c.host == "evil.example.com" {
			want = http.StatusForbidden
		}
		if rec.Code != want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	tr := newTrustRig(t, true)
	// the same Origin as the Host is the page itself
	if rec := tr.as(http.MethodPost, "/api/edit/edit1", local, "127.0.0.1:8790", map[string]string{"Origin": "http://127.0.0.1:8790", "Sec-Fetch-Site": "same-origin"}); rec.Code != http.StatusOK {
		t.Errorf("same origin: %d %s", rec.Code, rec.Body)
	}
	// a form post still cannot be a trusted save
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8790/api/edit/edit1", strings.NewReader("a=b"))
	req.RemoteAddr = local
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(nonceHeader, tr.ed.trust.nonce)
	rec := httptest.NewRecorder()
	tr.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form: %d", rec.Code)
	}
	// and a pre-flight is still refused
	req = httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:8790/api/edit/edit1", nil)
	req.RemoteAddr = local
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	tr.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight: %d %v", rec.Code, rec.Header())
	}
}

// The token is only ever on the server: no answer carries it, trusted or not, and the saved record names the label alone.
func TestTrustLocalNeverSendsTheToken(t *testing.T) {
	tr := newTrustRig(t, true)
	secret := []string{editToken, strings.Repeat("h", 40)}
	check := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		all := rec.Body.String()
		for k, v := range rec.Header() {
			all += k + ": " + strings.Join(v, ",") + "\n"
		}
		for _, s := range secret {
			if strings.Contains(all, s) {
				t.Errorf("%s: the token is in the answer", what)
			}
		}
	}
	local := "127.0.0.1:50000"
	check("save", tr.as(http.MethodPost, "/api/edit/edit1", local, "127.0.0.1:8790", nil))
	check("status", tr.as(http.MethodGet, "/api/edit/status", local, "127.0.0.1:8790", nil))
	check("versions", tr.as(http.MethodGet, "/api/edit/edit1/versions", local, "127.0.0.1:8790", nil))
	check("version", tr.as(http.MethodGet, "/api/edit/edit1/v0001", local, "127.0.0.1:8790", nil))
	check("refused", tr.as(http.MethodPost, "/api/edit/edit1", "198.51.100.7:1", "127.0.0.1:8790", nil))
	check("page", tr.as(http.MethodGet, "/", local, "127.0.0.1:8790", nil))
	for _, f := range []string{"app.js", "routeeditui.js", "routeedit.js"} {
		check(f, tr.as(http.MethodGet, "/"+f, local, "127.0.0.1:8790", nil))
	}
	st := tr.as(http.MethodGet, "/api/edit/status", local, "127.0.0.1:8790", nil)
	if !strings.Contains(st.Body.String(), `"local_trusted":true`) {
		t.Errorf("status for the local host: %s", st.Body)
	}
	if st := tr.as(http.MethodGet, "/api/edit/status", "198.51.100.7:1", trustSelf+":8790", nil); !strings.Contains(st.Body.String(), `"local_trusted":false`) {
		t.Errorf("status for a stranger: %s", st.Body)
	}
	// nothing the save wrote holds a token either
	for _, f := range []string{"v0001.json", "v0001.meta.json", "v0001.diff"} {
		if got := tree(t, filepath.Join(tr.ed.cfg.Store.Root(), "edit1")); !strings.Contains(got, f) {
			t.Fatalf("%s is not there: %s", f, got)
		}
	}
}

func TestEnableTrustLocalNeedsTheLabelAndTailnetPeers(t *testing.T) {
	r := newEditRig(t, nil)
	if err := r.ed.EnableTrustLocal("nobody", nil); err == nil {
		t.Error("an unknown label")
	}
	if err := r.ed.EnableTrustLocal("owner", []netip.Addr{netip.MustParseAddr("192.168.1.5")}); err == nil {
		t.Error("a LAN peer")
	}
	if r.ed.trust != nil {
		t.Error("trust set by a refused call")
	}
	peers, ignored := TrustPeers([]string{"100.64.0.1", "fd7a:115c:a1e0::5", "100.128.0.1", "10.0.0.1", "host.ts.net", "127.0.0.1", "::1", "localhost"})
	if len(peers) != 2 || len(ignored) != 4 {
		t.Errorf("peers %v ignored %v", peers, ignored)
	}
}
