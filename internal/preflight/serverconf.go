package preflight

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// SSHConf reads a test server's config over a read-only, BatchMode ssh call. The ssh host is the server name of the
// slot table (m910q, tortoise-test2): the owner's ssh config names the machines, while realmd's address can be a
// default-switch NAT address that only the host and its siblings reach.
type SSHConf struct {
	Host  func(server string) string // nil: the server name
	Paths []string                   // nil: the two test-server conf files
}

// ConfPaths are the files every test server has: the module's own keys and the AI playerbot keys.
var ConfPaths = []string{
	"~/tortoise-wow-server/etc/aiplayerbot.conf",
	"~/tortoise-wow-server/etc/modules/tortoise_bots.conf",
}

// EffectiveKeys reads both conf files and returns their effective keys: the last active "key = value" line of a
// file wins, commented lines do not count. A file missing on the server is skipped; no readable file is an error.
func (s SSHConf) EffectiveKeys(ctx context.Context, server string) (map[string]string, error) {
	host := server
	if s.Host != nil {
		host = s.Host(server)
	}
	paths := s.Paths
	if paths == nil {
		paths = ConfPaths
	}
	var script strings.Builder
	script.WriteString("for f in")
	for _, p := range paths {
		script.WriteString(" " + confShellPath(p))
	}
	script.WriteString(`; do printf '__FILE__ %s\n' "$f"; if [ -f "$f" ]; then cat "$f"; else printf '__MISSING__\n'; fi; done`)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR",
		host, "sh -c "+shellQuote(script.String()))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %v: %s", host, err, firstLine(string(out)))
	}
	keys, files := parseServerConf(string(out))
	if files == 0 {
		return nil, fmt.Errorf("ssh %s: no conf file found (%s)", host, strings.Join(paths, ", "))
	}
	return keys, nil
}

// parseServerConf reads the marked output of the ssh call into effective keys and counts the files read.
func parseServerConf(out string) (map[string]string, int) {
	keys := map[string]string{}
	files := 0
	inFile, counted := false, false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "__FILE__ "):
			inFile, counted = true, false
			continue
		case line == "__MISSING__":
			inFile = false
			continue
		}
		if !inFile {
			continue
		}
		if k, v, ok := confLine(line); ok {
			keys[k] = v
			if !counted {
				counted = true
				files++
			}
		}
	}
	return keys, files
}

// ParseConfLines is parseServerConf's key reader for one file's text, shared with the tests.
func ParseConfLines(text string) map[string]string {
	keys := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if k, v, ok := confLine(strings.TrimSuffix(line, "\r")); ok {
			keys[k] = v
		}
	}
	return keys
}

// confLine reads one active "key = value" line; section headers, comments and blanks are not keys.
func confLine(line string) (string, string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "[") {
		return "", "", false
	}
	i := strings.IndexByte(t, '=')
	if i <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(t[:i])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	v := strings.TrimSpace(t[i+1:])
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		v = v[1 : len(v)-1]
	}
	return key, v, true
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// confShellPath quotes a conf path for the remote shell: ~ is the remote login's home, so "$HOME/..." is used
// (a quoted ~ does not expand).
func confShellPath(p string) string {
	if p == "~" {
		return `"$HOME"`
	}
	if strings.HasPrefix(p, "~/") {
		return `"$HOME/` + p[2:] + `"`
	}
	return shellQuote(p)
}
