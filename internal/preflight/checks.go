package preflight

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/slots"
)

const runTimeout = 20 * time.Second

// checkBinaries is check 1: every binary exists, runs --help and prints a version; one walk may not mix binaries.
func (c *check) checkBinaries(ctx context.Context) {
	seen := map[string]bool{}
	for _, t := range c.targets {
		path := c.binaryPath(t.binary)
		if seen[path] {
			continue
		}
		seen[path] = true
		b := c.binaryFor(path)
		if fi, err := os.Stat(path); err != nil {
			c.add(Fail, CheckBinary, path, err.Error(), "build the binary or fix --binary/the command path")
			continue
		} else if fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
			c.add(Fail, CheckBinary, path, "not an executable file", "point the command at the built turtle-agent")
			continue
		}
		help, code, err := c.run(ctx, path, "--help")
		switch {
		case err != nil:
			c.add(Fail, CheckBinary, path, "does not start: "+err.Error(), "build the binary again")
			continue
		case strings.Contains(help, "panic:"):
			c.add(Fail, CheckBinary, path, "--help panicked: "+firstLine(help), "build the binary again")
			continue
		case code != 0 && code != 1 && code != 2:
			c.add(Fail, CheckBinary, path, fmt.Sprintf("--help exited %d", code), "the binary is broken: build it again")
			continue
		}
		b.help = help
		ver, vcode, verr := c.run(ctx, path, "version")
		first := firstLine(ver)
		if verr != nil || vcode != 0 {
			if verr != nil {
				c.add(Fail, CheckBinary, path, "version: "+verr.Error(), "build the binary again")
			} else {
				c.add(Fail, CheckBinary, path, fmt.Sprintf("version exited %d", vcode), "build the binary again")
			}
			continue
		}
		if first == "" {
			c.add(Fail, CheckBinary, path, "version printed nothing", "build the binary again")
			continue
		}
		commit := commitOf(path)
		b.ok = true
		detail := first
		if commit != "" {
			detail += " (commit " + commit + ")"
		} else {
			detail += " (no commit information)"
		}
		c.add(OK, CheckBinary, path, detail, "")
	}
	if len(seen) > 1 {
		paths := sortedKeys(seen)
		c.add(Warn, CheckBinary, "the walk", fmt.Sprintf("%d different binaries: %s", len(paths), strings.Join(paths, ", ")),
			"use one binary for every raid of the walk")
	}
}

// binaryPath resolves the binary a command names: a bare name from PATH, a path from the walk directory.
func (c *check) binaryPath(name string) string {
	if !strings.ContainsRune(name, os.PathSeparator) {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return c.expandPath(name)
}

// checkSchema is check 2: the binary parses slots.json (a no-op that loads the config, over a private copy so no
// lease or lock file is touched in the real folder).
func (c *check) checkSchema(ctx context.Context) {
	type group struct {
		binary, slotsPath, server string
	}
	var groups []group
	seen := map[string]bool{}
	for _, t := range c.targets {
		bin := c.binaryPath(t.binary)
		sp := c.slotsPathOf(t)
		key := bin + "\x00" + sp
		if seen[key] {
			continue
		}
		seen[key] = true
		groups = append(groups, group{binary: bin, slotsPath: sp, server: t.server})
	}
	for _, g := range groups {
		b := c.binaries[g.binary]
		if b == nil || !b.ok {
			continue
		}
		raw, err := os.ReadFile(g.slotsPath)
		if err != nil {
			c.add(Fail, CheckSchema, g.slotsPath, err.Error(), "fix the slot table path and permissions")
			continue
		}
		tmp, err := os.MkdirTemp("", "walk-preflight-")
		if err != nil {
			c.add(Fail, CheckSchema, g.slotsPath, err.Error(), "free space in $TMPDIR")
			continue
		}
		copyPath := filepath.Join(tmp, "slots.json")
		werr := os.WriteFile(copyPath, raw, 0o600)
		os.Chmod(tmp, 0o700)
		if werr != nil {
			os.RemoveAll(tmp)
			c.add(Fail, CheckSchema, g.slotsPath, werr.Error(), "free space in $TMPDIR")
			continue
		}
		out, code, rerr := c.run(ctx, g.binary, "lease", "list", "--slots", copyPath, g.server)
		os.RemoveAll(tmp)
		switch {
		case rerr != nil:
			c.add(Fail, CheckSchema, g.binary, "lease list: "+rerr.Error(), "build the binary again")
		case strings.Contains(strings.ToLower(out), "unknown field"):
			c.add(Fail, CheckSchema, g.binary, "slots.json has a field this binary does not know: "+firstLine(out),
				"use a binary that reads this slots.json, or remove the field")
		case code != 0:
			c.add(Fail, CheckSchema, g.binary, fmt.Sprintf("lease list exited %d: %s", code, firstLine(out)),
				"the binary cannot read "+g.slotsPath)
		default:
			c.add(OK, CheckSchema, g.binary, "reads "+g.slotsPath+" ("+g.server+")", "")
		}
	}
}

// checkSlotsPath is check 3: a --slots other than the default path splits the lease table from the cap ledger.
func (c *check) checkSlotsPath() {
	def := c.cleanPath(c.opts.SlotsPath)
	any := false
	for _, t := range c.targets {
		if t.slots == "" {
			continue
		}
		any = true
		got := c.cleanPath(c.expandPath(t.slots))
		if got == def {
			c.add(OK, CheckSlots, t.source, "--slots is the default "+def, "")
			continue
		}
		c.add(Fail, CheckSlots, t.source, fmt.Sprintf("--slots %s is not the default %s: it splits the lease table from the instance-cap ledger", got, def),
			"drop --slots (or make it the default path)")
	}
	if !any {
		c.add(OK, CheckSlots, "the walk", "no --slots override (the default is "+def+")", "")
	}
}

// checkInstanceCaps is check 4: print the cap each binary resolves, and fail a binary that would use the stock 5
// while instance-cap-per-account.json says more.
func (c *check) checkInstanceCaps() {
	type group struct {
		server, binary, slotsPath string
		flag                      int
		set                       bool
	}
	var groups []group
	seen := map[string]bool{}
	missingWarned := map[string]bool{}
	badFile := map[string]bool{}
	for _, t := range c.targets {
		bin := c.binaryPath(t.binary)
		sp := c.slotsPathOf(t)
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%v\x00%d", t.server, bin, sp, t.capSet, t.capFlag)
		if seen[key] {
			continue
		}
		seen[key] = true
		groups = append(groups, group{server: t.server, binary: bin, slotsPath: sp, flag: t.capFlag, set: t.capSet})
	}
	for _, g := range groups {
		caps, path, err := c.capsFor(g.slotsPath)
		if err != nil {
			if !badFile[path] {
				badFile[path] = true
				c.add(Fail, CheckCap, path, err.Error(), "fix the cap file (one number per server, 1 or more)")
			}
			continue
		}
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) && !missingWarned[path] {
			missingWarned[path] = true
			c.add(Warn, CheckCap, path, "missing: every binary uses the stock 5", "write {\"<server>\": <cap>} when the core is patched above 5")
		}
		b := c.binaries[g.binary]
		if b == nil || !b.ok {
			continue
		}
		support := strings.Contains(b.help, "--instance-cap-per-account")
		fileVal := caps[g.server]
		var effective int
		switch {
		case g.set && g.flag < 1:
			c.add(Fail, CheckCap, g.server+" ("+g.binary+")", fmt.Sprintf("--instance-cap-per-account %d is not 1 or more", g.flag),
				"drop the flag or give a real cap")
			continue
		case g.set && !support:
			c.add(Fail, CheckCap, g.server+" ("+g.binary+")", "--instance-cap-per-account is passed but this binary does not offer it",
				"use a binary that reads instance-cap-per-account.json, or drop the flag")
			continue
		case g.set:
			effective = slots.EffectiveInstanceCap(g.flag, caps, g.server)
		case support:
			effective = slots.EffectiveInstanceCap(0, caps, g.server)
		default:
			effective = slots.StockInstanceCap
		}
		if effective == slots.StockInstanceCap && fileVal > slots.StockInstanceCap {
			c.add(Fail, CheckCap, g.server+" ("+g.binary+")",
				fmt.Sprintf("this binary would use %d, but %s says %d for %s", effective, filepath.Base(path), fileVal, g.server),
				fmt.Sprintf("use a binary that reads %s, or pass --instance-cap-per-account %d", filepath.Base(path), fileVal))
			continue
		}
		if support && fileVal == 0 && len(caps) > 0 {
			c.add(Warn, CheckCap, g.server+" ("+g.binary+")",
				fmt.Sprintf("no %s entry: the binary uses the stock %d", g.server, effective),
				"add the server to "+filepath.Base(path))
			continue
		}
		c.add(OK, CheckCap, g.server+" ("+g.binary+")", fmt.Sprintf("effective cap %d", effective), "")
	}
}

// checkLeases is check 5: every server/slot is free or held by this walk, no two commands share one, never slot 3.
func (c *check) checkLeases() {
	cfg, err := c.slotsConfig()
	if err != nil {
		c.add(Fail, CheckLease, c.opts.SlotsPath, err.Error(), "fix the slot table and its permissions")
		return
	}
	owner := map[string]string{}
	anyServer := map[string][]string{}
	for _, t := range c.targets {
		if t.slot == "" {
			anyServer[t.server] = append(anyServer[t.server], t.source)
			continue
		}
		key := t.server + "/" + t.slot
		if src, ok := owner[key]; ok {
			c.add(Fail, CheckLease, key, fmt.Sprintf("two commands share it: %s and %s", src, t.source),
				"give each walk its own slot")
			continue
		}
		owner[key] = t.source
	}
	var anyServers []string
	for srv := range anyServer {
		anyServers = append(anyServers, srv)
	}
	sort.Strings(anyServers)
	for _, srv := range anyServers {
		srcs := anyServer[srv]
		if len(srcs) > 1 {
			sort.Strings(srcs)
			c.add(Warn, CheckLease, srv, fmt.Sprintf("%d commands have no --slot and compete for the free slots: %s", len(srcs), strings.Join(srcs, ", ")),
				"give each command a --slot")
		}
	}
	now := c.deps.Now()
	leasesDir := filepath.Join(filepath.Dir(c.opts.SlotsPath), "leases")
	for _, t := range c.targets {
		subj := t.source + " " + t.server + "/" + t.slot
		if t.slot == "" {
			free, held := 0, 0
			server := serverOf(cfg, t.server)
			if server == nil {
				c.add(Fail, CheckLease, subj, "unknown server "+t.server, "fix --server or slots.json")
				continue
			}
			for _, sl := range server.Slots {
				if sl.Reserved != "" || isReserved(t.server, sl.ID) {
					continue
				}
				l, err := readLease(leasesDir, t.server, sl.ID)
				if err != nil {
					held++
					continue
				}
				if l == nil {
					free++
				} else {
					held++
				}
			}
			if free == 0 {
				c.add(Fail, CheckLease, subj, fmt.Sprintf("no free slot on %s (%d held or reserved)", t.server, held),
					"wait for a slot or use another server")
			} else {
				c.add(OK, CheckLease, subj, fmt.Sprintf("any free slot (%d free)", free), "")
			}
			continue
		}
		if t.slot == "3" {
			c.add(Fail, CheckLease, subj, "slot 3 is never used (owner rule)", "pick another slot")
			continue
		}
		srv, sl, err := cfg.Find(t.server, t.slot)
		if err != nil {
			c.add(Fail, CheckLease, subj, err.Error(), "fix --server/--slot or slots.json")
			continue
		}
		if sl.Reserved != "" || isReserved(t.server, sl.ID) {
			c.add(Fail, CheckLease, subj, "slot is reserved: "+sl.Reserved, "pick another slot")
			continue
		}
		l, err := readLease(leasesDir, srv.Name, sl.ID)
		if err != nil {
			c.add(Fail, CheckLease, subj, "cannot read the lease: "+err.Error(), "fix the lease file")
			continue
		}
		if l == nil {
			c.add(OK, CheckLease, subj, "free", "")
			continue
		}
		pid, alive := 0, false
		if l.Dir != "" {
			pid, alive = c.deps.Probe.AgentPID(l.Dir)
		}
		stale := now.After(l.Expires) && !alive
		ours := l.Holder == t.holder && (t.out == "" || l.Dir == "" || underDir(l.Dir, c.expandPath(t.out)))
		switch {
		case ours && alive:
			c.add(OK, CheckLease, subj, fmt.Sprintf("held by this walk (pid %d, dir %s)", pid, l.Dir), "")
		case ours && !alive && !stale:
			c.add(Fail, CheckLease, subj, fmt.Sprintf("this walk's lease (holder %s) has no live agent (dir %s)", l.Holder, l.Dir),
				"run turtle-agent teardown on the old output dir")
		case stale:
			c.add(Warn, CheckLease, subj, fmt.Sprintf("stale lease of %s (expired %s): acquire takes it over", l.Holder, l.Expires.UTC().Format("15:04:05Z")),
				"nothing to do; the walk takes it over")
		default:
			pidText := "no live agent"
			if alive {
				pidText = fmt.Sprintf("agent pid %d running", pid)
			}
			c.add(Fail, CheckLease, subj, fmt.Sprintf("held by %s since %s, expires %s (%s)", l.Holder, l.Acquired.UTC().Format("15:04:05Z"), l.Expires.UTC().Format("15:04:05Z"), pidText),
				"wait for the holder or run teardown on its dir")
		}
	}
}

// checkPlacement is check 6: --gm-clear's spawns export exists, --place has --sql, and the --sql file exists.
func (c *check) checkPlacement() {
	relevant := false
	for _, t := range c.targets {
		if !t.place && !t.gmClear && !t.gmClearBoss && t.sql == "" {
			continue
		}
		relevant = true
		subj := t.source + " " + t.plan
		var problems, fixes, oks []string
		if t.place && t.sql == "" {
			problems = append(problems, "--place needs --sql")
			fixes = append(fixes, "add --sql <deployed migration>")
		}
		if t.sql != "" {
			p := c.expandPath(t.sql)
			if _, err := os.Stat(p); err != nil {
				problems = append(problems, "sql: "+err.Error())
				fixes = append(fixes, "deploy the migration or fix --sql")
			} else {
				oks = append(oks, "sql "+filepath.Base(p)+" exists")
			}
		}
		if t.gmClear || t.gmClearBoss {
			p, _, err := c.planOf(t)
			if err != nil {
				problems = append(problems, "cannot resolve the spawns dir: "+err.Error())
				fixes = append(fixes, "fix the plan the command loads")
			} else {
				spawns := t.gmClearSpawns
				if spawns == "" {
					spawns = filepath.Join(c.expandPath(t.dir), "..", "tools", "dungeongen", "testdata", fmt.Sprintf("map%d", p.Map))
				} else {
					spawns = c.expandPath(spawns)
				}
				spawns = filepath.Clean(spawns)
				if _, err := os.Stat(filepath.Join(spawns, "creatures.tsv")); err != nil {
					problems = append(problems, fmt.Sprintf("gm-clear spawns: no creatures.tsv in %s", spawns))
					fixes = append(fixes, "run tools/dungeongen/export.sh or pass --gm-clear-spawns")
				} else {
					oks = append(oks, "spawns "+spawns)
				}
			}
		}
		switch {
		case len(problems) > 0:
			c.add(Fail, CheckPlace, subj, strings.Join(problems, "; "), strings.Join(fixes, "; "))
		default:
			c.add(OK, CheckPlace, subj, strings.Join(oks, "; "), "")
		}
	}
	if !relevant {
		c.add(OK, CheckPlace, "the walk", "no --place, --gm-clear or --sql in the commands", "")
	}
}

var sqlStampRe = regexp.MustCompile(`^([0-9]{14})_world\.sql$`)

// checkPlans is check 7: the plan JSON the binary loads matches the --sql export (map, legs, source rev, stamp).
func (c *check) checkPlans() {
	seen := map[string]bool{}
	for _, t := range c.targets {
		key := t.plan + "\x00" + t.dir + "\x00" + t.sql
		if seen[key] {
			continue
		}
		seen[key] = true
		subj := t.plan
		p, planPath, err := c.planOf(t)
		if err != nil {
			c.add(Fail, CheckPlan, subj, err.Error(), "fix --dir/<plan>.json")
			continue
		}
		if t.sql == "" {
			c.add(OK, CheckPlan, subj, fmt.Sprintf("%s: map %d, %s (no --sql to compare)", planPath, p.Map, pl(len(p.Legs))), "")
			continue
		}
		sqlPath := c.expandPath(t.sql)
		rows, err := c.migrationOf(sqlPath)
		if err != nil {
			c.add(Fail, CheckPlan, subj, "cannot read the export: "+err.Error(), "fix --sql")
			continue
		}
		var exp *planexportRows
		for i := range rows {
			if rows[i].Plan.Plan == p.Name {
				r := rows[i]
				exp = &planexportRows{mapID: r.Plan.Map, legs: r.Legs, source: r.Plan.Source}
				break
			}
		}
		if exp == nil {
			c.add(Warn, CheckPlan, subj, fmt.Sprintf("the export %s has no plan %s", filepath.Base(sqlPath), p.Name), "export the plan into the migration, or fix --sql")
			continue
		}
		var problems []string
		if exp.mapID != p.Map {
			problems = append(problems, fmt.Sprintf("map: export %d, plan %d", exp.mapID, p.Map))
		}
		planLegs := map[string]int{}
		order := map[string]int{}
		for i, l := range p.Legs {
			planLegs[l.Name] = i
			order[l.Name] = i
		}
		last := -1
		for _, l := range exp.legs {
			idx, ok := planLegs[l.Name]
			if !ok {
				problems = append(problems, fmt.Sprintf("export leg %q is not in the plan", l.Name))
				continue
			}
			if idx < last {
				problems = append(problems, fmt.Sprintf("export leg %q is out of the plan's order", l.Name))
			}
			last = idx
		}
		if base := filepath.Base(sqlPath); sqlStampRe.MatchString(base) {
			stamp, _ := time.ParseInLocation(planexport.StampFormat, sqlStampRe.FindStringSubmatch(base)[1], time.UTC)
			if fi, err := os.Stat(planPath); err == nil && fi.ModTime().After(stamp.Add(time.Minute)) {
				problems = append(problems, fmt.Sprintf("the plan was edited %s, after the export %s", fi.ModTime().UTC().Format("2006-01-02 15:04"), stamp.Format("2006-01-02 15:04")))
			}
		}
		if exp.source != "" {
			fields := strings.Fields(exp.source)
			if len(fields) >= 3 {
				rev, srcPath := fields[1], fields[len(fields)-1]
				if filepath.Base(srcPath) != p.Name+".json" {
					problems = append(problems, fmt.Sprintf("the export's source names %s, not %s.json", srcPath, p.Name))
				}
				if head := c.gitHead(c.expandPath(t.dir)); head != "" && strings.TrimSuffix(rev, "+local") != head {
					problems = append(problems, fmt.Sprintf("export built from %s, the plan dir is at %s", rev, head))
				}
			}
		}
		if len(problems) > 0 {
			c.add(Warn, CheckPlan, subj, strings.Join(problems, "; "), "export the plan again (turtle-agent plan-export) and redeploy it")
			continue
		}
		c.add(OK, CheckPlan, subj, fmt.Sprintf("%s matches %s: map %d, %s", planPath, filepath.Base(sqlPath), p.Map, pl(len(exp.legs))), "")
	}
}

// planexportRows is the part of an exported plan this check compares.
type planexportRows struct {
	mapID  uint32
	legs   []planexport.LegRow
	source string
}

// checkServerKeys is check 8: the server's effective aiplayerbot.conf / tortoise_bots.conf lines match the required
// keys. Values of keys that name a password, key or secret are never printed.
func (c *check) checkServerKeys(ctx context.Context) {
	raw, err := os.ReadFile(c.opts.KeysPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.add(Warn, CheckKeys, c.opts.KeysPath, "missing: the server keys are not checked", "write {\"keys\":{\"AiPlayerbot.X\":\"1\"}}")
			return
		}
		c.add(Fail, CheckKeys, c.opts.KeysPath, err.Error(), "fix the file's permissions")
		return
	}
	var want struct {
		Keys map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		c.add(Fail, CheckKeys, c.opts.KeysPath, err.Error(), "write {\"keys\":{\"AiPlayerbot.X\":\"1\"}}")
		return
	}
	if len(want.Keys) == 0 {
		c.add(OK, CheckKeys, c.opts.KeysPath, "no required keys", "")
		return
	}
	servers := map[string]bool{}
	for _, t := range c.targets {
		servers[t.server] = true
	}
	keys := sortedStrings(want.Keys)
	for _, server := range sortedKeys(servers) {
		conf, err := c.deps.Conf.EffectiveKeys(ctx, server)
		if err != nil {
			c.add(Fail, CheckKeys, server, "cannot read the server config: "+err.Error(),
				"check `ssh -o BatchMode=yes "+server+"` and the conf paths")
			continue
		}
		var bad []string
		for _, key := range keys {
			wantVal := want.Keys[key]
			got, ok := conf[key]
			switch {
			case !ok:
				bad = append(bad, key+": absent (want "+maskValue(key, wantVal)+")")
			case got != wantVal:
				bad = append(bad, key+": have "+maskValue(key, got)+", want "+maskValue(key, wantVal))
			}
		}
		if len(bad) > 0 {
			c.add(Fail, CheckKeys, server, strings.Join(bad, "; "), "set the keys in aiplayerbot.conf / tortoise_bots.conf on "+server)
			continue
		}
		c.add(OK, CheckKeys, server, fmt.Sprintf("%d required keys match", len(keys)), "")
	}
}

// checkRosters is check 9: the roster files parse, and a targeted Arlokk or Hakkar has at least 3 tanks.
func (c *check) checkRosters() {
	any := false
	warned := map[string]bool{}
	summarized := map[string]bool{}
	for _, t := range c.targets {
		if t.hires == "" {
			continue
		}
		any = true
		path := c.expandPath(t.hires)
		r, err := c.rosterOf(path)
		if err != nil {
			c.add(Fail, CheckRoster, path, err.Error(), "fix the roster file (tb-raid-roster v1/v2)")
			continue
		}
		p, _, perr := c.planOf(t)
		if perr == nil {
			if err := r.Validate(p); err != nil {
				c.add(Fail, CheckRoster, path, err.Error(), "fix the roster against the plan")
				continue
			}
		}
		tanks := r.TankSeats()
		if perr == nil {
			for _, rule := range bossRules {
				if rule.plan != p.Name {
					continue
				}
				idx, leg, ok := bossLeg(p, rule.entry)
				if !ok || !fightsBoss(t, p, idx) {
					continue
				}
				key := path + "\x00" + strconv.FormatUint(uint64(rule.entry), 10)
				if tanks >= 3 || warned[key] {
					continue
				}
				warned[key] = true
				c.add(Warn, CheckRoster, path,
					fmt.Sprintf("hires %d tank%s, but this command targets %s (%d, leg %s)",
						tanks, plural(tanks), rule.name, rule.entry, leg),
					"add tanks to the roster (at least 3 for this boss)")
			}
		}
		if !summarized[path] {
			summarized[path] = true
			c.add(OK, CheckRoster, path, fmt.Sprintf("%d bots, %d tank%s", r.Total(), tanks, plural(tanks)), "")
		}
	}
	if !any {
		c.add(OK, CheckRoster, "the walk", "no --hires: each walk uses the plan's default roster", "")
	}
}

// bossRules are the bosses whose roster tank count the brief names.
var bossRules = []struct {
	plan  string
	entry uint32
	name  string
}{
	{"zul_gurub", 14515, "Arlokk"},
	{"zul_gurub", 14834, "Hakkar"},
}

// fightsBoss says whether the command will fight the boss in leg bossIdx: the walk targets it from its --from and
// --until (raid-walk's default --until boss stops before the first boss, so no boss is fought).
func fightsBoss(t target, p *dungeon.Plan, bossIdx int) bool {
	if t.goalBoss != 0 {
		idx, _, ok := bossLeg(p, t.goalBoss)
		return ok && idx == bossIdx
	}
	start := 0
	if t.from != "" {
		if name, ok := strings.CutPrefix(t.from, "leg "); ok {
			idx, found := legIndex(p, name)
			if !found {
				return true // an unknown leg name: the walk refuses it, but do not hide the warning
			}
			start = idx
		} else {
			return true // a step start: be conservative
		}
	}
	until := t.until
	if until == "" {
		until = "boss"
	}
	if t.goalLeg != "" {
		until = "leg " + t.goalLeg
	}
	switch {
	case until == "none":
		return bossIdx >= start
	case until == "boss":
		return false
	case until == "boss-done":
		return bossIdx == firstBossAfter(p, start)
	case strings.HasPrefix(until, "leg "):
		idx, found := legIndex(p, strings.TrimPrefix(until, "leg "))
		if !found {
			return true
		}
		return bossIdx >= start && bossIdx <= idx
	default:
		return true // a step until: conservative
	}
}

func (c *check) rosterOf(path string) (*raidwalk.Roster, error) {
	if r, ok := c.rosters[path]; ok {
		return r, c.rosterErrs[path]
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		var r *raidwalk.Roster
		r, err = raidwalk.ParseRoster(raw)
		c.rosters[path] = r
	}
	c.rosterErrs[path] = err
	return c.rosters[path], c.rosterErrs[path]
}

// helpers

func (c *check) run(ctx context.Context, path string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	return c.deps.Run(ctx, path, args...)
}

func (c *check) slotsPathOf(t target) string {
	if t.slots != "" {
		return c.expandPath(t.slots)
	}
	return c.opts.SlotsPath
}

func (c *check) cleanPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if e, err := filepath.EvalSymlinks(abs); err == nil {
		return e
	}
	return abs
}

// gitHead is the plan folder's short commit, "" when it is not a checkout.
func (c *check) gitHead(dir string) string {
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	out, code, err := c.run(context.Background(), "git", "-C", dir, "rev-parse", "--short=8", "HEAD")
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

func readLease(dir, server, slot string) (*slots.Lease, error) {
	raw, err := os.ReadFile(filepath.Join(dir, server, slot+".json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l slots.Lease
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

func underDir(dir, base string) bool {
	d := dir
	b := base
	if a, err := filepath.Abs(b); err == nil {
		b = a
	}
	if a, err := filepath.Abs(d); err == nil {
		d = a
	}
	return d == b || strings.HasPrefix(d, b+string(os.PathSeparator))
}

func serverOf(cfg *slots.Config, name string) *slots.Server {
	for i := range cfg.Servers {
		if cfg.Servers[i].Name == name {
			return &cfg.Servers[i]
		}
	}
	return nil
}

func isReserved(server, id string) bool {
	return (server == "m910q" || server == "tortoise-test2") && id == "3"
}

func bossLeg(p *dungeon.Plan, entry uint32) (int, string, bool) {
	for i := range p.Legs {
		if p.Legs[i].Boss != nil && p.Legs[i].Boss.Entry == entry {
			return i, p.Legs[i].Name, true
		}
	}
	return 0, "", false
}

func firstBossAfter(p *dungeon.Plan, start int) int {
	for i := start; i < len(p.Legs); i++ {
		if p.Legs[i].Boss != nil {
			return i
		}
	}
	return -1
}

func legIndex(p *dungeon.Plan, name string) (int, bool) {
	for i := range p.Legs {
		if p.Legs[i].Name == name {
			return i, true
		}
	}
	return 0, false
}

// commitOf reads a Go binary's VCS revision, "" when it carries none.
func commitOf(path string) string {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	rev, modified := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" && modified {
		rev += "+dirty"
	}
	return rev
}

func maskValue(key, value string) string {
	up := strings.ToUpper(key)
	if strings.Contains(up, "PASS") || strings.Contains(up, "KEY") || strings.Contains(up, "SECRET") {
		if value == "" {
			return "(hidden, empty)"
		}
		return "(hidden)"
	}
	return value
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func sortedStrings(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
