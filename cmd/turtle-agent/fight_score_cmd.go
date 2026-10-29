package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/kbspells"
	"github.com/pjerra/turtle-agent/internal/tracecheck"
)

const fightScoreUsage = "usage: turtle-agent fight-score [--json] [--mt NAME] [--ot A,B] [--set spell_oracle=S] <combat-fight.json>...\n" +
	"  scores finished fights the way the dashboard's fight debugger reads them (mt_sunder, ot_taunt) and judges each bot\n" +
	"  against its KB spec's spell lists; exit 0 (warnings never fail), 2 on usage"

// fightJSON is the fight the dashboard stores: the daemon's combat.Fight, or a bare one.
type fightJSON struct {
	ID        string    `json:"id"`
	Commander string    `json:"commander"`
	DurMS     int64     `json:"dur_ms"`
	Bots      []fighter `json:"bots"`
}

type fighter struct {
	Name   string       `json:"name"`
	Class  string       `json:"class"`
	Role   string       `json:"role"`
	Spells []fightSpell `json:"spells"`
}

type fightSpell struct {
	Name string `json:"n"`
	N    int    `json:"c"`
}

// scoreRow is one line of the reading: the debugger's check ids plus the oracle's per-bot findings keep the same shape.
type scoreRow struct {
	ID    string `json:"id"`
	Level string `json:"level"`
	Num   string `json:"num"`
	Text  string `json:"text"`
	Bot   string `json:"bot,omitempty"`
}

type fightScore struct {
	Fight     string             `json:"fight"`
	Commander string             `json:"commander,omitempty"`
	DURMS     int64              `json:"dur_ms"`
	MT        *scoreRow          `json:"mt_sunder,omitempty"`
	OTs       []scoreRow         `json:"ot_taunt"`
	Oracle    []kbspells.Finding `json:"oracle"`
}

// fightScoreCmd reads finished fight JSON (the debugger's input: {fight, warnings} or a bare fight) and prints the fight
// debugger's two tank checks with its exact rules, then one oracle line per bot with a warning. It reads files only and never
// logs in.
func fightScoreCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fight-score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the reading as JSON")
	mtName := fs.String("mt", "", "main tank name (default: the first tank in the fight)")
	otNames := fs.String("ot", "", "off-tank names, comma separated (default: the other tanks)")
	var sets thresholdFlags
	fs.Var(&sets, "set", "change a threshold: check=value (spell_oracle)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, fightScoreUsage)
		return exitUsage
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, fightScoreUsage)
		return exitUsage
	}
	cfg := tracecheck.Defaults()
	cfg.SpellOracleActiveS = 20 // a fight is judged after 20 s of it, not a trace's minute
	for _, s := range sets {
		k, v, ok := strings.Cut(s, "=")
		n, err := strconv.ParseFloat(v, 64)
		if !ok || err != nil {
			fmt.Fprintf(stderr, "--set %q: want check=number\n", s)
			return exitUsage
		}
		if err := cfg.Set(k, n); err != nil {
			fmt.Fprintln(stderr, "--set:", err)
			return exitUsage
		}
	}
	oracle := kbspells.Default()
	for _, path := range fs.Args() {
		f, err := readFightFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			return exitUsage
		}
		r := scoreFight(f, *mtName, *otNames, cfg, oracle)
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(r)
			continue
		}
		printFightScore(stdout, f, r)
	}
	return exitOK
}

func readFightFile(path string) (*fightJSON, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env struct {
		Fight json.RawMessage `json:"fight"`
	}
	if err := json.Unmarshal(b, &env); err == nil && len(env.Fight) > 0 {
		b = env.Fight
	}
	var f fightJSON
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("not a fight: %w", err)
	}
	if f.DurMS <= 0 && len(f.Bots) == 0 {
		return nil, fmt.Errorf("not a fight: no duration and no bots")
	}
	return &f, nil
}

func scoreFight(f *fightJSON, mtName, otNames string, cfg tracecheck.Config, o *kbspells.Oracle) fightScore {
	r := fightScore{Fight: f.ID, Commander: f.Commander, DURMS: f.DurMS}
	mt := mainTank(f, mtName)
	r.MT = sunderRow(f, mt, o.Common.Limits)
	for _, b := range offTanks(f, mt, otNames) {
		r.OTs = append(r.OTs, tauntRow(b, o.Common.Taunts))
	}
	active := float64(f.DurMS) / 1000
	for _, b := range f.Bots {
		casts := map[string]int{}
		for _, s := range b.Spells {
			casts[s.Name] += s.N
		}
		r.Oracle = append(r.Oracle, o.Judge(kbspells.BotCasts{
			Name: b.Name, Class: b.Class, Role: b.Role, Casts: casts,
			Truncated: len(b.Spells) >= 8, ActiveS: active,
		}, cfg.SpellOracleActiveS))
	}
	return r
}

// mainTank is the debugger's rule: the named bot if it is a tank in the fight, else the first tank in the fight's order.
func mainTank(f *fightJSON, name string) *fighter {
	if name != "" {
		if b := fighterNamed(f, name); b != nil && isTank(*b) {
			return b
		}
	}
	for i := range f.Bots {
		if isTank(f.Bots[i]) {
			return &f.Bots[i]
		}
	}
	return nil
}

// offTanks: the named bots (in the given order), or with no names the tanks other than the main tank.
func offTanks(f *fightJSON, mt *fighter, names string) []*fighter {
	var out []*fighter
	if strings.TrimSpace(names) != "" {
		for _, n := range strings.Split(names, ",") {
			if b := fighterNamed(f, strings.TrimSpace(n)); b != nil {
				out = append(out, b)
			}
		}
		return out
	}
	for i := range f.Bots {
		b := &f.Bots[i]
		if isTank(*b) && b != mt {
			out = append(out, b)
		}
	}
	return out
}

func fighterNamed(f *fightJSON, name string) *fighter {
	for i := range f.Bots {
		if strings.EqualFold(f.Bots[i].Name, name) {
			return &f.Bots[i]
		}
	}
	return nil
}

func isTank(b fighter) bool { return strings.EqualFold(b.Role, "tank") }

func spellCount(b *fighter, name string) int {
	n := 0
	for _, s := range b.Spells {
		if canonicalName(s.Name) == canonicalName(name) {
			n += s.N
		}
	}
	return n
}

// canonicalName matches the fight's spell names case-insensitively without changing the displayed name.
func canonicalName(n string) string { return strings.ToLower(strings.TrimSpace(n)) }

// sunderRow is the debugger's mt_sunder: a warrior main tank casting Sunder Armor at least sunder_ok times per 10 s passes, at
// least sunder_warn warns, below fails; a fight under min_fight_ms is not judged.
func sunderRow(f *fightJSON, mt *fighter, lim kbspells.Limits) *scoreRow {
	row := &scoreRow{ID: "mt_sunder", Num: "-"}
	switch {
	case mt == nil:
		row.Level, row.Text = "na", "no tank in the fight"
		return row
	case !strings.EqualFold(mt.Class, "warrior"):
		row.Level, row.Text = "na", fmt.Sprintf("main tank %s is a %s, not a warrior", mt.Name, mt.Class)
		return row
	case f.DurMS < lim.MinFightMS:
		row.Level, row.Text = "na", fmt.Sprintf("fight of %.0f s is under %.0f s", float64(f.DurMS)/1000, float64(lim.MinFightMS)/1000)
		return row
	}
	sunder := spellCount(mt, "Sunder Armor")
	shield := spellCount(mt, "Shield Slam")
	secs := float64(f.DurMS) / 1000
	rate := float64(sunder) / secs * 10
	row.Level, row.Num, row.Bot = "fail", fmt.Sprintf("%.1f/10 s", rate), mt.Name
	if rate >= lim.SunderOKPer10s {
		row.Level = "ok"
	} else if rate >= lim.SunderWarnPer10s {
		row.Level = "warn"
	}
	row.Text = fmt.Sprintf("%s (%s) cast Sunder Armor %dx in %.0f s; Shield Slam %dx", mt.Name, mt.Class, sunder, secs, shield)
	return row
}

// tauntRow is the debugger's ot_taunt: an off-tank that taunted at all passes, one that never taunted warns.
func tauntRow(b *fighter, taunts []string) scoreRow {
	row := scoreRow{ID: "ot_taunt", Level: "warn", Num: "0x", Bot: b.Name}
	var used []string
	total := 0
	for _, s := range b.Spells {
		for _, t := range taunts {
			if strings.EqualFold(s.Name, t) {
				used = append(used, fmt.Sprintf("%s %dx", s.Name, s.N))
				total += s.N
			}
		}
	}
	row.Num = fmt.Sprintf("%dx", total)
	if total > 0 {
		row.Level = "ok"
		row.Text = fmt.Sprintf("%s (%s): %s", b.Name, b.Class, strings.Join(used, ", "))
	} else {
		row.Text = fmt.Sprintf("%s (%s): no taunt", b.Name, b.Class)
	}
	return row
}

func printFightScore(w io.Writer, f *fightJSON, r fightScore) {
	fmt.Fprintf(w, "fight %s %s %.0f s, %d bots\n", r.Fight, r.Commander, float64(f.DurMS)/1000, len(f.Bots))
	if r.MT != nil {
		printScoreRow(w, *r.MT)
	}
	if len(r.OTs) == 0 {
		printScoreRow(w, scoreRow{ID: "ot_taunt", Level: "na", Num: "-", Text: "no off-tank in the fight"})
	}
	for _, row := range r.OTs {
		printScoreRow(w, row)
	}
	ok, na := 0, 0
	for _, x := range r.Oracle {
		switch x.Level {
		case "warn":
			printScoreRow(w, scoreRow{ID: "oracle", Level: "warn", Num: "-", Text: oracleTextOf(x), Bot: x.Bot})
		case "ok":
			ok++
		default:
			na++
		}
	}
	text := fmt.Sprintf("%d bot(s) judged ok", ok)
	if na > 0 {
		text += fmt.Sprintf(", %d not judged", na)
	}
	printScoreRow(w, scoreRow{ID: "oracle", Level: "ok", Num: "-", Text: text})
}

func printScoreRow(w io.Writer, r scoreRow) {
	fmt.Fprintf(w, "%-10s %-4s %-9s %s\n", r.ID, r.Level, r.Num, r.Text)
}

// oracleTextOf is a finding as one line, the same words the trace report uses.
func oracleTextOf(f kbspells.Finding) string {
	return fmt.Sprintf("%s (%s %s): %s", f.Bot, f.Class, f.Role, f.Text)
}
