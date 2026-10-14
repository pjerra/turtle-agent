package dashboard

import (
	"embed"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/pjerra/turtle-agent/internal/netutil"
)

//go:embed static
var staticFS embed.FS

// Handler serves the page, the cached state and the files of the tests folder. Every route is GET or HEAD; nothing in it sends
// anything to an agent. allowHosts are the Host names the guard accepts besides the loopback names (see AllowedHosts).
func Handler(p *Poller, testsDir string, allowHosts []string, embedFrom ...string) http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	static := http.FileServer(http.FS(sub))
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, p.State())
	})
	mux.HandleFunc("/api/raw/", func(w http.ResponseWriter, r *http.Request) {
		b, ok := p.Raw(strings.TrimPrefix(r.URL.Path, "/api/raw/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, b)
	})
	mux.HandleFunc("/api/plan/", func(w http.ResponseWriter, r *http.Request) {
		geo, errText := p.Geometry(strings.TrimPrefix(r.URL.Path, "/api/plan/"))
		if geo == nil {
			http.Error(w, errText, http.StatusNotFound)
			return
		}
		writeJSON(w, geo)
	})
	mux.HandleFunc("/api/minimap/", func(w http.ResponseWriter, r *http.Request) {
		serveMinimapLayers(w, r, p.opt.MinimapDir)
	})
	mux.HandleFunc("/minimap/", func(w http.ResponseWriter, r *http.Request) {
		serveMinimapImage(w, r, p.opt.MinimapDir)
	})
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		serveTestFile(w, r, testsDir)
	})
	mux.Handle("/", static)
	return guard(mux, FrameAncestors(embedFrom), hostSet(allowHosts))
}

func writeJSON(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// FrameAncestors is the CSP frame-ancestors list: this page, loopback pages (the TortoiseBots observability dashboard on the same
// machine embeds it), and the extra origins the operator named with --embed-from. Origins are checked by CheckEmbedOrigin.
func FrameAncestors(extra []string) string {
	parts := []string{"'self'", "http://127.0.0.1:*", "http://localhost:*", "http://[::1]:*"}
	return strings.Join(append(parts, extra...), " ")
}

// CheckEmbedOrigin accepts an http or https origin (scheme, host, optional port; no path, query or credentials), the only
// shape that is safe to put into a CSP source list.
func CheckEmbedOrigin(o string) error {
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(o, " ;,'\"") {
		return fmt.Errorf("%q is not an origin like http://host:port", o)
	}
	return nil
}

// AllowedHosts is what the guard accepts in a Host header besides the loopback names: the names of list (the --allow-host value:
// host names or IP addresses, comma separated) and, when listenHost is a specific non-loopback IP, that IP, so that a page served
// on a tailnet address opens by it. A name is exact (no wildcard, port, scheme or path); 0.0.0.0 and :: name no host a browser
// can reach, so they are refused.
func AllowedHosts(listenHost, list string) ([]string, error) {
	var out []string
	for _, h := range strings.Split(list, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
			h = h[1 : len(h)-1] // an IPv6 literal, as a Host header writes it
		}
		if ip := net.ParseIP(h); ip != nil {
			if ip.IsUnspecified() {
				return nil, fmt.Errorf("%q is not an address a browser can name", h)
			}
		} else if !validHostName(h) {
			return nil, fmt.Errorf("%q is not a host name or IP address (no port, scheme, path or wildcard)", h)
		}
		out = append(out, h)
	}
	if ip := net.ParseIP(listenHost); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
		out = append(out, listenHost)
	}
	return out, nil
}

// validHostName reports whether h is a DNS name of letters, digits and hyphens (labels of 1 to 63 characters, no leading or
// trailing hyphen), with an optional trailing dot.
func validHostName(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// hostKey is a Host name as the guard compares it: an IP address in its canonical form, a name in lower case without a
// trailing dot.
func hostKey(h string) string {
	if ip := net.ParseIP(h); ip != nil {
		return ip.String()
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// hostSet is the set of the allowed names, by hostKey.
func hostSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, h := range names {
		if k := hostKey(h); k != "" {
			set[k] = true
		}
	}
	return set
}

// guard refuses what a read-only local page has no use for: other methods, and requests whose Host is neither a loopback name
// nor one of allowed (a page on the web cannot reach the dashboard by rebinding a name to 127.0.0.1: its Host is the name).
func guard(next http.Handler, ancestors string, allowed map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		host = strings.Trim(host, "[]")
		if !netutil.IsLoopback(host) && !allowed[hostKey(host)] {
			http.Error(w, "loopback host names only (or a name given with --allow-host)", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors "+ancestors)
		next.ServeHTTP(w, r)
	})
}

// textTypes are served as plain text so that a browser shows them instead of downloading them.
var textTypes = map[string]bool{".jsonl": true, ".log": true, ".txt": true, ".md": true, ".json": true, ".cap": true}

func serveTestFile(w http.ResponseWriter, r *http.Request, root string) {
	if root == "" {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/files/")
	full, err := SafeJoin(root, rel)
	if err != nil {
		http.NotFound(w, r) // not why: a probe learns nothing about what is outside
		return
	}
	ext := strings.ToLower(path.Ext(full))
	ctype := "text/plain; charset=utf-8"
	if !textTypes[ext] {
		if t := mime.TypeByExtension(ext); t != "" {
			ctype = t
		} else {
			ctype = "application/octet-stream"
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, full)
}
