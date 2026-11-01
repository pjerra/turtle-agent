package raidwalk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// Roster is the tb-raid-roster v1 file: the batches hired, in order, and the level and tier.
type Roster struct {
	Format  string  `json:"format"`
	Version int     `json:"version"`
	Level   int     `json:"level,omitempty"`
	Tier    int     `json:"tier,omitempty"`
	Batches []Batch `json:"batches"`
}

// Batch is one hire order: an mt, an ot or a rest batch.
type Batch struct {
	Role string `json:"role"`
	Hire string `json:"hire"`

	tokens []hireToken
}

// The roles a batch may have.
const (
	RoleMT   = "mt"
	RoleOT   = "ot"
	RoleRest = "rest"
)

// hireToken is one entry of a hire grammar: a count with a class and an optional role, or a role count.
type hireToken struct {
	Count int
	Class string
	Role  string // tank, healer or dps; "" when the class carries its own default
}

const (
	// maxRosterBots is the most a roster may ask for: every seat of a 40-player raid (the commander stands outside
	// the group, so all 40 are hires; amendment G2).
	maxRosterBots = 40
	maxHireTier   = 3
	// HireCap is the most the TortoiseBots server hires into one commander's raid today: its
	// HireMaxBotsPerPlayer stops at 39 (onyxia-kill-plan.md C6), so a 40-bot roster is asked for and its 40th seat
	// comes up short with a summary note, never an error (amendment G2).
	HireCap = 39
)

var (
	hireCountRe  = regexp.MustCompile(`^[0-9]+`)
	hireRoleWord = map[string]string{"t": "tank", "h": "healer", "d": "dps"}
	hireClass    = map[string]bool{
		"warrior": true, "paladin": true, "hunter": true, "rogue": true, "priest": true,
		"shaman": true, "mage": true, "warlock": true, "druid": true,
	}
	hireRole = map[string]bool{"tank": true, "healer": true, "dps": true}
)

var errPresetHire = errors.New("a preset fills the raid in one go; name the tanks in an mt batch")

// ParseRoster reads and checks a tb-raid-roster v1 file. It needs no plan: the raid-size bound is checked by
// Validate, after the plan is loaded.
func ParseRoster(data []byte) (*Roster, error) {
	var r Roster
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	if r.Format != "tb-raid-roster" {
		return nil, fmt.Errorf("roster format %q, want tb-raid-roster", r.Format)
	}
	if r.Version != 1 {
		return nil, fmt.Errorf("roster version %d, want 1", r.Version)
	}
	if err := r.check(nil); err != nil {
		return nil, err
	}
	return &r, nil
}

// Validate checks the roster against the plan's raid size and fills the default level (the plan's level band max).
func (r *Roster) Validate(p *dungeon.Plan) error {
	return r.check(p)
}

func (r *Roster) check(p *dungeon.Plan) error {
	if r.Level < 0 || r.Level > 60 {
		return fmt.Errorf("roster level %d, want 1-60", r.Level)
	}
	if r.Tier < 0 || r.Tier > maxHireTier {
		return fmt.Errorf("roster tier %d, want 1-3 or absent", r.Tier)
	}
	if len(r.Batches) == 0 {
		return errors.New("roster: no batches")
	}

	mt, ot, rest := 0, 0, 0
	mtAt := -1
	total := 0
	for i := range r.Batches {
		b := &r.Batches[i]
		if b.Hire == "" {
			return fmt.Errorf("roster batch %d has no hire", i+1)
		}
		tokens, n, err := validateHire(b.Hire)
		if err != nil {
			return fmt.Errorf("roster batch %d (%s): %w", i+1, b.Role, err)
		}
		b.tokens = tokens
		total += n
		switch b.Role {
		case RoleMT:
			mt++
			mtAt = i
			if len(tokens) != 1 || tokens[0].Count != 1 || tokens[0].Role != "tank" {
				return errors.New("the mt batch hires exactly 1 bot whose role is tank (1 <class>:tank or 1t)")
			}
		case RoleOT:
			ot++
			for _, t := range tokens {
				if t.Role != "tank" {
					return errors.New("the ot batch hires tanks only (every token t or :tank)")
				}
			}
		case RoleRest:
			rest++
		default:
			return fmt.Errorf("roster batch %d: role %q, want mt, ot or rest", i+1, b.Role)
		}
	}
	if mt != 1 {
		return fmt.Errorf("the roster needs exactly one mt batch, first; it has %d", mt)
	}
	if mtAt != 0 {
		return errors.New("the roster's mt batch must be first")
	}
	if ot > 1 {
		return fmt.Errorf("the roster has %d ot batches, want at most one", ot)
	}
	if rest == 0 {
		return errors.New("the roster needs at least one rest batch")
	}
	if total < 1 || total > maxRosterBots {
		return fmt.Errorf("the roster hires %d bots, want 1-%d", total, maxRosterBots)
	}
	if p != nil {
		if p.Raid == nil {
			return fmt.Errorf("plan %s has no raid block", p.Name)
		}
		if p.Raid.Size < 2 {
			return fmt.Errorf("plan %s: raid size %d", p.Name, p.Raid.Size)
		}
		if total > p.Raid.Size {
			return fmt.Errorf("the roster hires %d bots; plan %s has %d raid slots and its commander stands outside, so at most %d",
				total, p.Name, p.Raid.Size, p.Raid.Size)
		}
		if r.Level == 0 {
			if len(p.LevelBand) < 2 {
				return fmt.Errorf("plan %s gives no level_band; set the roster's level", p.Name)
			}
			r.Level = p.LevelBand[1]
		}
	}
	return nil
}

// validateHire reads the hire grammar: <n>t, <n>h, <n>d, or "<n> <class>[:<role>]", counts 1-40. A bare word is a
// preset and refused.
func validateHire(s string) ([]hireToken, int, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, 0, errors.New("empty hire")
	}
	var out []hireToken
	total := 0
	for i := 0; i < len(fields); i++ {
		tok := fields[i]
		if m := hireCountRe.FindString(tok); m != "" {
			n, err := strconv.Atoi(m)
			if err != nil || n < 1 || n > maxRosterBots {
				return nil, 0, fmt.Errorf("%q: the count must be 1-%d", tok, maxRosterBots)
			}
			if suffix := tok[len(m):]; suffix != "" {
				role, ok := hireRoleWord[suffix]
				if !ok {
					return nil, 0, fmt.Errorf("%q: use <n>t, <n>h or <n>d", tok)
				}
				out = append(out, hireToken{Count: n, Role: role})
				total += n
				continue
			}
			if i+1 >= len(fields) {
				return nil, 0, fmt.Errorf("%q is a count without a class; write %s warrior:tank or %st", tok, tok, tok)
			}
			i++
			next := fields[i]
			class, role, _ := strings.Cut(next, ":")
			if class == "" || !hireClass[class] {
				return nil, 0, fmt.Errorf("%q is not a class I can hire", class)
			}
			if role != "" && !hireRole[role] {
				return nil, 0, fmt.Errorf("%q is not a role; use tank, healer or dps", role)
			}
			out = append(out, hireToken{Count: n, Class: class, Role: role})
			total += n
			continue
		}
		if strings.Contains(tok, ":") || hireClass[tok] || hireRoleWord[tok] != "" {
			return nil, 0, fmt.Errorf("%q has no count", tok)
		}
		return nil, 0, errPresetHire
	}
	return out, total, nil
}

// Total is the number of bots the batches hire.
func (r *Roster) Total() int {
	total := 0
	for i := range r.Batches {
		if r.Batches[i].tokens == nil {
			_, n, err := validateHire(r.Batches[i].Hire)
			if err != nil {
				continue
			}
			total += n
			continue
		}
		for _, t := range r.Batches[i].tokens {
			total += t.Count
		}
	}
	return total
}

// HireLines is the hire command of each batch, in order.
func (r *Roster) HireLines() []string {
	out := make([]string, 0, len(r.Batches))
	for _, b := range r.Batches {
		out = append(out, r.HireLine(b.Hire))
	}
	return out
}

// HireLine is the hire command for one batch string, in HireLines' form: the
// retry of a short batch rebuilds its line the same way.
func (r *Roster) HireLine(hire string) string {
	line := fmt.Sprintf(".bot hire raid %s level=%d", hire, r.Level)
	if r.Tier > 0 {
		line += fmt.Sprintf(" t%d", r.Tier)
	}
	return line
}

// RoleCounts reads a t/h/d batch hire string ("5h 12d") into per-role counts. A
// class batch ("1 warrior:tank") reports false: it names exact bots and is never
// retried piecemeal.
func RoleCounts(hire string) (tanks, healers, dps int, ok bool) {
	tokens, _, err := validateHire(hire)
	if err != nil {
		return 0, 0, 0, false
	}
	for _, t := range tokens {
		if t.Class != "" {
			return 0, 0, 0, false
		}
		switch t.Role {
		case "tank":
			tanks += t.Count
		case "healer":
			healers += t.Count
		case "dps":
			dps += t.Count
		default:
			return 0, 0, 0, false
		}
	}
	return tanks, healers, dps, true
}

// DefaultRoster builds the roster of spec 3.2 from the plan's raid block: an mt of one warrior tank, the other
// tanks as an ot batch, and the healers and dps as one rest batch — every raid slot of the plan (amendment G2; the
// commander stands outside the group). Its tier is the committed table's row, 0 for a raid the table leaves unknown
// (the walk then needs --tier N). The plan's numbers win (walk 3's manual roster named one tank fewer than Onyxia's
// plan).
func DefaultRoster(p *dungeon.Plan) (*Roster, error) {
	if p == nil || p.Raid == nil {
		return nil, errors.New("the plan has no raid block")
	}
	size, tanks, healers := p.Raid.Size, p.Raid.Tanks, p.Raid.Healers
	if size < 2 || tanks < 1 || healers < 0 || tanks+healers > size {
		return nil, fmt.Errorf("plan %s: raid size %d, tanks %d, healers %d do not make a party", p.Name, size, tanks, healers)
	}
	level := 0
	if len(p.LevelBand) >= 2 {
		level = p.LevelBand[1]
	}
	r := &Roster{
		Format:  "tb-raid-roster",
		Version: 1,
		Level:   level,
		Tier:    MinimumHireTier[p.Name],
		Batches: []Batch{{Role: RoleMT, Hire: "1 warrior:tank"}},
	}
	if tanks > 1 {
		r.Batches = append(r.Batches, Batch{Role: RoleOT, Hire: fmt.Sprintf("%dt", tanks-1)})
	}
	dps := size - tanks - healers
	var rest []string
	if healers > 0 {
		rest = append(rest, fmt.Sprintf("%dh", healers))
	}
	if dps > 0 {
		rest = append(rest, fmt.Sprintf("%dd", dps))
	}
	if len(rest) == 0 {
		return nil, fmt.Errorf("plan %s: no healers or dps to hire", p.Name)
	}
	r.Batches = append(r.Batches, Batch{Role: RoleRest, Hire: strings.Join(rest, " ")})
	if err := r.check(p); err != nil {
		return nil, err
	}
	return r, nil
}
