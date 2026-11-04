// raid_walk_accounts.go: the pool accounts of the hired party, for the instance ledger. The core's cap is five fresh
// instance ids per ACCOUNT per hour; the hire reply does not name them, so the walk reads them with a GM probe
// (`.pinfo <hire>`, read-only) after the hires and records them on the ledger entry. An entry that carries them is
// counted per account; one that does not keeps the older conservative raid count (instcap.go).
package main

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

const (
	// accountReadTimeout bounds the whole account read after the hires. A server that refuses `.pinfo` costs this
	// once and the walk goes on without accounts (the ledger then counts raids, as before).
	accountReadTimeout = 20 * time.Second
	// accountReadIdle is how long the read keeps polling after the last new account came in (and bounds a server
	// that refuses `.pinfo`, so an unsupported probe costs seconds, not the whole timeout).
	accountReadIdle = 5 * time.Second
)

var (
	// pinfoAccountRe matches the account id in the core's LANG_PINFO_ACCOUNT line ("... Account: <link>
	// (id: 42, banned) GMLevel: ..."). The account NAME is scrubbed by the session's redactor; the id is not, and
	// per-account counting needs no name.
	pinfoAccountRe = regexp.MustCompile(`Account: .*?\(id: ([0-9]+)`)
	// pinfoNameRe matches the first player link, the probed character (the account's own link follows it).
	pinfoNameRe = regexp.MustCompile(`\|Hplayer:([^|]+)\|h\[`)
)

// parsePInfoLine reads the character and account id out of a `.pinfo` reply's first line. A line that is not that
// line (the money, mail and fingerprint follow-ups) reports ok false.
func parsePInfoLine(text string) (name, account string, ok bool) {
	a := pinfoAccountRe.FindStringSubmatch(text)
	if a == nil {
		return "", "", false
	}
	if n := pinfoNameRe.FindStringSubmatch(text); n != nil {
		name = strings.TrimSpace(n[1])
	}
	return name, a[1], true
}

// readHireAccounts sends `.pinfo <hire>` for every hire and reads the replies until each hire has an account or the
// deadline passes. It never fails the walk: a missing reply leaves that hire's account unknown, and the ledger
// entry then keeps the raid count. The returned accounts are unique and sorted, so the ledger's file is stable.
func (w *walk) readHireAccounts(ctx context.Context) []string {
	if w.env.capLedger == "" || len(w.hires) == 0 {
		return nil
	}
	names := make([]string, 0, len(w.hires))
	for n := range w.hires {
		names = append(names, n)
	}
	sort.Strings(names)
	sent := 0
	for _, n := range names {
		if err := w.gm(ctx, ".pinfo "+n); err != nil {
			w.note("instance cap: .pinfo %s was refused (%v); the entry will not name that account", n, err)
			break
		}
		sent++
	}
	if sent == 0 {
		return nil
	}
	found := map[string]string{} // character -> account id
	deadline := w.env.now().Add(accountReadTimeout)
	lastNew := w.env.now()
	for len(found) < sent && w.env.now().Before(deadline) {
		var page events.Page
		params := map[string]any{"since": w.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": 1000}
		if err := w.agent.Call(ctx, "events", params, &page, 15*time.Second); err != nil {
			w.note("instance cap: the account read stopped: %v", err)
			break
		}
		w.eventsAt = page.Next
		for _, e := range page.Events {
			if line, ok := tbm.Line(e); ok {
				w.logLine(w.env.now(), line)
				continue
			}
			if e.Kind != events.KindSystem || e.Text == "" {
				continue
			}
			text := raidwalk.CleanLine(e.Text)
			name, account, ok := parsePInfoLine(text)
			if !ok || name == "" || !w.hires[name] {
				continue
			}
			w.logLine(w.env.now(), "instance cap: hire "+name+" is on account id "+account)
			if _, have := found[name]; !have {
				lastNew = w.env.now()
			}
			found[name] = account
		}
		if len(found) >= sent || w.env.now().Sub(lastNew) >= accountReadIdle {
			break
		}
		if err := w.env.sleep(ctx, 200*time.Millisecond); err != nil {
			break
		}
	}
	unique := map[string]bool{}
	for _, account := range found {
		unique[account] = true
	}
	out := make([]string, 0, len(unique))
	for a := range unique {
		out = append(out, a)
	}
	sort.Strings(out)
	if len(found) < len(names) {
		w.note("instance cap: %d of %d hires' accounts were read; the entry names %d account(s)", len(found), len(names), len(out))
	}
	return out
}
