package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
}

func newTrustRig(t *testing.T, on bool) *trustRig {
	t.Helper()
	r := newEditRig(t, nil)
	if on {
		peers, _ := TrustPeers([]string{trustSelf, trustPeer, "laptop.tail1234.ts.net", "192.168.1.5", "127.0.0.1"})
		must(t, r.ed.EnableTrustLocal("owner", peers))
	}
	allow := []string{trustSelf, trustPeer, trustOther, "laptop.tail1234.ts.net", "192.168.1.5", "203.0.113.9"}
	return &trustRig{r, HandlerWithEditor(r.p, "", allow, nil, r.ed)}
}

// as sends a request as if its TCP peer were remote, to Host host, with extra headers; the body is a valid edit of the test plan.
func (tr *trustRig) as(method, path, remote, host string, hdr map[string]string) *httptest.ResponseRecorder {
	var body []byte
	if method == http.MethodPost {
		body, _ = json.Marshal(tr.edit(move(0, 0, 21, 1)))
	}
	req := httptest.NewRequest(method, "http://"+host+path, bytes.NewReader(body))
	req.RemoteAddr = remote
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	tr.h.ServeHTTP(rec, req)
	return rec
}

func TestTrustLocalAcceptsTheLocalHostAndTheNamedTailnetPeers(t *testing.T) {
	tr := newTrustRig(t, true)
	for name, c := range map[string]struct{ remote, host string }{
		"loopback v4":                 {"127.0.0.1:50000", "127.0.0.1:8790"},
		"loopback v6":                 {"[::1]:50000", "localhost:8790"},
		"loopback mapped":             {"[::ffff:127.0.0.1]:50000", "localhost:8790"},
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
	if tree(t, tr.edits) != before {
		t.Error("a refused request wrote")
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
