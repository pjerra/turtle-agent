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
// anything to an agent.
func Handler(p *Poller, testsDir string, embedFrom ...string) http.Handler {
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
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		serveTestFile(w, r, testsDir)
	})
	mux.Handle("/", static)
	return guard(mux, FrameAncestors(embedFrom))
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

// guard refuses what a read-only local page has no use for: other methods, and requests whose Host is not a loopback name (a
// page on the web cannot reach the dashboard by rebinding a name to 127.0.0.1).
func guard(next http.Handler, ancestors string) http.Handler {
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
		if !netutil.IsLoopback(strings.Trim(host, "[]")) {
			http.Error(w, "loopback host names only", http.StatusForbidden)
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
