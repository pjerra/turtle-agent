package tracecheck

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/trace"
)

// The spell-usage report: what each class of the party cast and did, from the frames of an agent trace. It is a report, not a verdict:
// the "spells" check always passes (or is n/a when the trace has no casts or actions) and the thresholds below only produce warnings.
//
// What the trace can and cannot show (1 Hz frames): a bot frame has the spell it is casting or channelling (cs), and its newest action
// verdict (act, cut to 16 bytes by the module; res and why since the verdict is kept). Instants, melee specials and casts shorter than
// the gap between two frames are not seen, and the same action chosen in two frames in a row counts once. Whether an interrupt or a
// crowd control landed is read from the mob's cast and cc flags and is a reading, not a fact.

// IDSpells is the id of the report-only spell-usage check.
const IDSpells = "spells"

const (
	actNameBytes   = 16   // the module cuts the action name to this many bytes
	lowManaPct     = 10.0 // a mana user below this share of its mana in combat is "dry"
	idleRunSeconds = 4.0  // a verdict that is not O for this long, with a hostile on the party, is idle
	idleMinFrames  = 30   // fewer engaged frames and the idle share is not measured
	minCastsRate   = 5    // fewer enemy casts and the interrupt cover is not measured
	minCCRate      = 3    // fewer crowd-control casts and their success rate is not measured
	ccWindowS      = 4.0  // seconds after a crowd-control action in which the target must show the cc flag
	cutShortOf     = 0.8  // a cast seen for less than this share of its cast time was cut short
)

// SpellReport is the report; Lines prints it. Fields are exported for --json.
type SpellReport struct {
	Frames      int          `json:"frames"`       // frames with bots
	CastsSeen   bool         `json:"casts_seen"`   // any bot frame has a cs
	ActsSeen    bool         `json:"acts_seen"`    // any bot frame has an act
	VerdictSeen bool         `json:"verdict_seen"` // any bot frame has the verdict letter (res)
	Classes     []ClassSpell `json:"classes"`
	Idle        IdleShare    `json:"idle"`
	Deaths      []DeathMana  `json:"deaths,omitempty"`
	DryBots     int          `json:"dry_bots"` // mana users that went under 10 % mana in combat
	Interrupt   InterruptUse `json:"interrupt"`
	Taunts      TauntUse     `json:"taunts"`
	CC          CCUse        `json:"cc"`
	Warnings    []string     `json:"warnings,omitempty"`
}

// ClassSpell is one class's casts, action mix and verdicts.
type ClassSpell struct {
	Class    string         `json:"class"`
	Bots     []string       `json:"bots"`
	Casts    int            `json:"casts"`
	TopCasts []NameCount    `json:"top_casts,omitempty"`
	Mix      []NameCount    `json:"mix,omitempty"`      // combat frames by action category (counts)
	Verdicts map[string]int `json:"verdicts,omitempty"` // combat frames by verdict letter
	Whys     []NameCount    `json:"whys,omitempty"`     // reasons of the failed or impossible verdicts
	Frames   int            `json:"combat_frames"`
}

// NameCount is a name and how many times.
type NameCount struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// IdleShare is the share of engaged frames a mana user spent without an executed action.
type IdleShare struct {
	Recorded bool               `json:"recorded"`
	Engaged  int                `json:"engaged_frames"`
	Idle     int                `json:"idle_frames"`
	Pct      float64            `json:"pct"`
	ByClass  map[string]float64 `json:"by_class,omitempty"`
}

// DeathMana is a death with the dead bot's power just before it.
type DeathMana struct {
	Name     string  `json:"name"`
	Class    string  `json:"class"`
	AtMS     int64   `json:"t"`
	Power    string  `json:"power"`           // "mana", "rage", "focus", "energy"
	AtDeath  float64 `json:"at_death_pct"`    // percent of its maximum
	AtStart  float64 `json:"fight_start_pct"` // percent when the fight began
	Min      float64 `json:"fight_min_pct"`
	HPAtLast float64 `json:"hp_pct"`
	Seen     bool    `json:"seen"` // false: no frame of the bot before the death
}

// InterruptUse is the enemy casts seen and the interrupts that met them.
type InterruptUse struct {
	Casts     int         `json:"enemy_casts"`
	Covered   int         `json:"with_interrupt"` // an interrupt action ran within a second of the cast
	CutShort  int         `json:"cut_short"`      // of those, the cast was seen for under 80 % of its cast time
	Pct       float64     `json:"cover_pct"`
	ByBot     []NameCount `json:"by_bot,omitempty"`
	TopMissed []NameCount `json:"top_uncovered,omitempty"`
}

// TauntUse counts the taunts.
type TauntUse struct {
	Done  int         `json:"done"`  // verdict O, or any verdict when none is recorded
	Tried int         `json:"tried"` // all of them
	ByBot []NameCount `json:"by_bot,omitempty"`
}

// CCUse is the crowd-control casts and how many showed the cc flag on their target.
type CCUse struct {
	Casts     int         `json:"casts"`
	Evaluable int         `json:"evaluable"` // the target was in the frames
	Landed    int         `json:"landed"`
	Pct       float64     `json:"pct"`
	BySpell   []NameCount `json:"by_spell,omitempty"`
}

// ---- name tables ----

// classOf maps an action or spell name (lower case) to a class. An entry also matches any name that starts with it (the longest entry
// wins), so "seal of" holds every seal, and a name the module cut to 16 bytes matches the entry it is the start of.
var classTable = map[string]string{}

// category is what an action does; categoryTable lists the action-name prefixes in one place.
const (
	CatHeal      = "heal"
	CatDamage    = "damage"
	CatCC        = "cc"
	CatInterrupt = "interrupt"
	CatTaunt     = "taunt"
	CatDefense   = "defense"
	CatBuff      = "buff"
	CatMove      = "move"
	CatStay      = "stay"
	CatRest      = "rest"
	CatPet       = "pet"
	CatThreat    = "threat"
	CatAdmin     = "admin"
	CatOther     = "other"
)

type catEntry struct {
	prefix string
	cat    string
	single bool // cc: one target, so the target's cc flag shows whether it landed
}

var categoryList = []catEntry{
	// interrupts
	{"kick", CatInterrupt, false}, {"pummel", CatInterrupt, false}, {"shield bash", CatInterrupt, false}, {"counterspell", CatInterrupt, false},
	{"earth shock", CatInterrupt, false}, {"spell lock", CatInterrupt, false},
	// taunts
	{"taunt", CatTaunt, false}, {"challenging shout", CatTaunt, false}, {"mocking blow", CatTaunt, false}, {"growl", CatTaunt, false},
	{"challenging roar", CatTaunt, false}, {"hand of reckoning", CatTaunt, false}, {"righteous defense", CatTaunt, false},
	// crowd control
	{"polymorph", CatCC, true}, {"shackle undead", CatCC, true}, {"freezing trap", CatCC, true}, {"sap", CatCC, true}, {"banish", CatCC, true},
	{"fear", CatCC, true}, {"seduction", CatCC, true}, {"hibernate", CatCC, true}, {"entangling roots", CatCC, true}, {"scatter shot", CatCC, true},
	{"gouge", CatCC, true}, {"blind", CatCC, true}, {"hammer of justice", CatCC, true}, {"turn undead", CatCC, true}, {"death coil", CatCC, true},
	{"frost nova", CatCC, false}, {"howl of terror", CatCC, false}, {"psychic scream", CatCC, false}, {"concussive shot", CatCC, false},
	{"wing clip", CatCC, false}, {"hamstring", CatCC, false}, {"cheap shot", CatCC, true}, {"kidney shot", CatCC, true}, {"bash", CatCC, true},
	// defense and escapes
	{"shield wall", CatDefense, false}, {"last stand", CatDefense, false}, {"shield block", CatDefense, false}, {"evasion", CatDefense, false},
	{"vanish", CatDefense, false}, {"feint", CatDefense, false}, {"fade", CatDefense, false}, {"feign death", CatDefense, false},
	{"ice block", CatDefense, false}, {"blink", CatDefense, false}, {"disengage", CatDefense, false}, {"desperate prayer", CatDefense, false},
	{"divine shield", CatDefense, false}, {"power word: shield", CatHeal, false}, {"bloodrage", CatBuff, false},
	// heals (a heal is "<spell> on party" in the feed: the suffix is cut before the lookup)
	{"flash heal", CatHeal, false}, {"greater heal", CatHeal, false}, {"lesser heal", CatHeal, false}, {"heal", CatHeal, false},
	{"renew", CatHeal, false}, {"prayer of healing", CatHeal, false}, {"holy light", CatHeal, false}, {"flash of light", CatHeal, false},
	{"healing touch", CatHeal, false}, {"rejuvenation", CatHeal, false}, {"regrowth", CatHeal, false}, {"healing wave", CatHeal, false},
	{"lesser healing wave", CatHeal, false}, {"chain heal", CatHeal, false}, {"mend pet", CatPet, false}, {"health funnel", CatHeal, false},
	{"resurrection", CatHeal, false}, {"redemption", CatHeal, false}, {"revive", CatHeal, false}, {"ancestral spirit", CatHeal, false},
	{"cure", CatHeal, false}, {"dispel", CatHeal, false}, {"abolish", CatHeal, false}, {"purify", CatHeal, false}, {"cleanse", CatHeal, false},
	{"remove curse", CatHeal, false}, {"first aid", CatRest, false}, {"bandage", CatRest, false},
	// rest
	{"drink", CatRest, false}, {"eat", CatRest, false}, {"food", CatRest, false}, {"life tap", CatRest, false}, {"evocation", CatRest, false},
	{"mana gem", CatRest, false}, {"conjure", CatRest, false},
	// pets
	{"pet ", CatPet, false}, {"call pet", CatPet, false}, {"revive pet", CatPet, false}, {"summon", CatPet, false},
	// movement and stance
	{"reach", CatMove, false}, {"move", CatMove, false}, {"follow", CatMove, false}, {"flee", CatMove, false}, {"hunter step out", CatMove, false},
	{"release pull", CatMove, false}, {"set facing", CatMove, false}, {"charge", CatMove, false}, {"intervene", CatMove, false},
	{"stay", CatStay, false}, {"wait", CatStay, false},
	// buffs
	{"blessing of", CatBuff, false}, {"power word: fortitude", CatBuff, false}, {"prayer of fortitude", CatBuff, false}, {"divine spirit", CatBuff, false},
	{"inner fire", CatBuff, false}, {"arcane intellect", CatBuff, false}, {"frost armor", CatBuff, false}, {"ice armor", CatBuff, false},
	{"mage armor", CatBuff, false}, {"mark of the wild", CatBuff, false}, {"thorns", CatBuff, false}, {"demon", CatBuff, false}, {"aspect of", CatBuff, false},
	{"battle shout", CatBuff, false}, {"seal of", CatBuff, false}, {"devotion aura", CatBuff, false}, {"defensive stance", CatBuff, false},
	{"battle stance", CatBuff, false}, {"berserker", CatBuff, false}, {"stealth", CatBuff, false}, {"berserking", CatBuff, false},
	{"ascendance", CatBuff, false}, {"inner focus", CatBuff, false}, {"trueshot aura", CatBuff, false}, {"lightning shield", CatBuff, false},
	{"hunter's mark", CatBuff, false}, {"poison", CatBuff, false},
	// threat tools of a tank
	{"sunder armor", CatThreat, false}, {"revenge", CatThreat, false}, {"shield slam", CatThreat, false}, {"thunder clap", CatThreat, false},
	{"demoralizing shout", CatThreat, false}, {"concussion blow", CatThreat, false}, {"holy shield", CatThreat, false}, {"righteous fury", CatThreat, false},
	{"disarm", CatThreat, false},
	// damage
	{"frostbolt", CatDamage, false}, {"fireball", CatDamage, false}, {"arcane", CatDamage, false}, {"aimed shot", CatDamage, false},
	{"steady shot", CatDamage, false}, {"multi-shot", CatDamage, false}, {"auto shot", CatDamage, false}, {"shoot", CatDamage, false},
	{"serpent sting", CatDamage, false}, {"viper sting", CatDamage, false}, {"scorpid sting", CatDamage, false}, {"raptor strike", CatDamage, false},
	{"mongoose bite", CatDamage, false}, {"shadow bolt", CatDamage, false}, {"immolate", CatDamage, false}, {"corruption", CatDamage, false},
	{"curse of", CatDamage, false}, {"drain", CatDamage, false}, {"shadowburn", CatDamage, false}, {"searing pain", CatDamage, false},
	{"soul fire", CatDamage, false}, {"conflagrate", CatDamage, false}, {"siphon life", CatDamage, false}, {"smite", CatDamage, false},
	{"holy fire", CatDamage, false}, {"shadow word", CatDamage, false}, {"mind blast", CatDamage, false}, {"mind flay", CatDamage, false},
	{"holy nova", CatDamage, false}, {"fire blast", CatDamage, false}, {"flamestrike", CatDamage, false}, {"scorch", CatDamage, false},
	{"blizzard", CatDamage, false}, {"cone of cold", CatDamage, false}, {"lightning bolt", CatDamage, false}, {"flame shock", CatDamage, false},
	{"frost shock", CatDamage, false}, {"moonfire", CatDamage, false}, {"wrath", CatDamage, false}, {"starfire", CatDamage, false},
	{"maul", CatDamage, false}, {"swipe", CatDamage, false}, {"rake", CatDamage, false}, {"claw", CatDamage, false}, {"shred", CatDamage, false},
	{"rip", CatDamage, false}, {"melee", CatDamage, false}, {"sinister strike", CatDamage, false}, {"eviscerate", CatDamage, false},
	{"backstab", CatDamage, false}, {"ambush", CatDamage, false}, {"hemorrhage", CatDamage, false}, {"riposte", CatDamage, false},
	{"slice and dice", CatDamage, false}, {"judgement", CatDamage, false}, {"consecration", CatDamage, false}, {"crusader strike", CatDamage, false},
	{"exorcism", CatDamage, false}, {"holy wrath", CatDamage, false}, {"heroic strike", CatDamage, false}, {"rend", CatDamage, false},
	{"overpower", CatDamage, false}, {"mortal strike", CatDamage, false}, {"cleave", CatDamage, false}, {"execute", CatDamage, false},
	{"slam", CatDamage, false}, {"whirlwind", CatDamage, false}, {"shadowflame", CatDamage, false}, {"cower", CatDefense, false},
	// the engine's own bookkeeping actions
	{"add gathering", CatAdmin, false}, {"check values", CatAdmin, false}, {"formation", CatAdmin, false}, {"select new", CatAdmin, false},
	{"dps assist", CatAdmin, false}, {"xp gain", CatAdmin, false}, {"mount anim", CatAdmin, false}, {"jump", CatAdmin, false},
	{"apply stone", CatAdmin, false}, {"move to position", CatMove, false}, {"set behind", CatMove, false}, {"tank face", CatMove, false},
	{"wait for attack", CatStay, false},
}

// classKeys are the names that tell a class, listed by class.
var classKeys = map[string][]string{
	"warrior": {"sunder armor", "shield bash", "heroic strike", "revenge", "shield slam", "shield block", "thunder clap", "taunt", "challenging shout",
		"battle shout", "demoralizing shout", "disarm", "bloodrage", "charge", "last stand", "shield wall", "mocking blow", "intervene", "pummel",
		"defensive stance", "concussion blow", "rend", "overpower", "mortal strike", "cleave", "execute", "hamstring", "berserker rage", "slam", "whirlwind"},
	"priest": {"flash heal", "heal", "lesser heal", "greater heal", "renew", "power word: shield", "smite", "holy fire", "shadow word: pain", "mind blast",
		"prayer of healing", "fade", "dispel magic", "cure disease", "abolish disease", "shackle undead", "psychic scream", "inner fire", "holy nova",
		"desperate prayer", "inner focus", "mind flay", "power word: fortitude", "prayer of fortitude", "divine spirit"},
	"mage": {"frostbolt", "fireball", "arcane missiles", "frost nova", "cone of cold", "blink", "polymorph", "counterspell", "arcane explosion", "blizzard",
		"evocation", "ice block", "fire blast", "flamestrike", "scorch", "conjure", "mana gem", "frost armor", "ice armor", "mage armor", "arcane intellect"},
	"warlock": {"shadow bolt", "immolate", "corruption", "curse of", "drain life", "drain soul", "life tap", "fear", "howl of terror", "death coil", "shadowburn",
		"searing pain", "summon imp", "summon voidwalker", "summon succubus", "summon felhunter", "health funnel", "demon skin", "demon armor", "spell lock", "banish",
		"soul fire", "conflagrate", "siphon life", "seduction", "create healthstone", "create soulstone"},
	"hunter": {"auto shot", "aimed shot", "arcane shot", "steady shot", "multi-shot", "serpent sting", "hunter's mark", "concussive shot", "raptor strike",
		"mongoose bite", "wing clip", "feign death", "freezing trap", "scatter shot", "mend pet", "call pet", "revive pet", "volley", "disengage", "aspect of",
		"distracting shot", "scare beast", "pet attack", "hunter step out", "viper sting", "scorpid sting"},
	"rogue": {"sinister strike", "eviscerate", "kick", "gouge", "sap", "blind", "vanish", "evasion", "feint", "backstab", "slice and dice", "riposte",
		"kidney shot", "cheap shot", "ambush", "stealth", "ghostly strike", "hemorrhage", "deadly poison", "instant poison", "crippling poison"},
	"shaman": {"lightning bolt", "earth shock", "healing wave", "lesser healing wave", "chain heal", "flame shock", "frost shock", "lightning shield",
		"searing totem", "stoneskin totem", "grounding totem", "windfury", "stormstrike", "ancestral spirit"},
	"paladin": {"holy light", "flash of light", "judgement", "seal of", "blessing of", "consecration", "hammer of justice", "crusader strike", "holy shield",
		"righteous fury", "devotion aura", "divine shield", "lay on hands", "purify", "cleanse", "redemption", "exorcism", "holy wrath", "hand of reckoning"},
	"druid": {"healing touch", "rejuvenation", "regrowth", "moonfire", "wrath", "starfire", "entangling roots", "thorns", "mark of the wild", "bear form",
		"maul", "swipe", "growl", "bash", "hibernate", "rake", "claw", "shred", "rip", "cat form", "innervate", "remove curse"},
}

func init() {
	for c, ks := range classKeys {
		for _, k := range ks {
			// A name two classes share (taunt-like, shared dispels) is left out, so it tells nothing.
			if old, ok := classTable[k]; ok && old != c {
				classTable[k] = ""
				continue
			}
			classTable[k] = c
		}
	}
	// The entries are matched longest first.
	sort.SliceStable(categoryList, func(i, j int) bool { return len(categoryList[i].prefix) > len(categoryList[j].prefix) })
}

// normalizeAction lower-cases an action name and cuts the suffixes the feed adds ("heal on party", "polymorph on cc").
func normalizeAction(n string) string {
	n = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(n)), "_ ", ": ") // the feed writes "power word_ shield"
	for _, suf := range []string{" on party", " on cc", " on target", " on attacker", " on enemy"} {
		n = strings.TrimSuffix(n, suf)
	}
	return n
}

// keyMatch says whether name starts with key, or is the module's 16-byte cut of a name that does.
func keyMatch(name, key string) bool {
	return strings.HasPrefix(name, key) || (len(name) >= actNameBytes && strings.HasPrefix(key, name))
}

// ClassOfName gives the class an action or spell name belongs to ("" when it tells none).
func ClassOfName(n string) string {
	n = normalizeAction(n)
	if n == "" {
		return ""
	}
	best, bestLen := "", -1
	for k, c := range classTable {
		if c != "" && keyMatch(n, k) && len(k) > bestLen {
			best, bestLen = c, len(k)
		}
	}
	return best
}

// CategoryOf gives the category of an action name (longest prefix first) and whether it is a single-target crowd control.
func CategoryOf(n string) (cat string, single bool) {
	n = normalizeAction(n)
	if n == "" {
		return CatOther, false
	}
	for _, e := range categoryList {
		if keyMatch(n, e.prefix) {
			return e.cat, e.single
		}
	}
	return CatOther, false
}

// ---- the report ----

type actEv struct {
	t         int64
	g         string
	name, res string
	target    string
	why       string
}

type mobCast struct {
	g           string
	spell       uint32
	first, last int64
	closed      bool
}

type fightRun struct {
	start, min float64
}

// spellBot is what the pass keeps of a bot between frames.
type spellBot struct {
	cs      uint32
	act     string
	res     string
	idleRun []int64 // times of the frames of the current idle run
	fight   *fightRun
	lastT   int64
	last    trace.Bot // its last frame while alive
	seen    bool
	wasDead bool // a frame showed it dead since its last fight: the next fight is a new one
}

// deathMana reads a death: the bot's power in its last living frame, and the mana at the start and the lowest in that fight.
func deathMana(f *trace.File, e trace.Event, st map[string]*spellBot, classOf map[string]string) DeathMana {
	g := str(e.D, "a")
	name := str(e.D, "name")
	if name == "" {
		name = f.Name(g)
	}
	d := DeathMana{Name: name, Class: classOf[g], AtMS: e.T}
	s := st[g]
	if s == nil || !s.seen {
		return d
	}
	d.Seen = true
	b := s.last
	d.Power = [...]string{"mana", "rage", "focus", "energy"}[min(int(b.PType), 3)]
	if b.MaxPow > 0 {
		d.AtDeath = round1(float64(b.Power) / float64(b.MaxPow) * 100)
	}
	if b.MaxHP > 0 {
		d.HPAtLast = round1(float64(b.HP) / float64(b.MaxHP) * 100)
	}
	d.AtStart, d.Min = d.AtDeath, d.AtDeath
	if s.fight != nil {
		d.AtStart, d.Min = round1(s.fight.start), round1(s.fight.min)
	}
	return d
}

// Spells builds the report of a trace.
func Spells(f *trace.File, c Config) SpellReport {
	r := SpellReport{}
	interval := f.Header.IntervalMS
	if interval <= 0 {
		interval = 1000
	}
	ivS := float64(interval) / 1000

	// Pass 1: what is recorded, the party guids, and the class of each bot (the party record, else the votes of its action and spell names).
	party := map[string]bool{}
	for _, m := range f.Party {
		party[m.G] = true
	}
	votes := map[string]map[string]int{}
	vote := func(g, name string) {
		if cl := ClassOfName(name); cl != "" {
			if votes[g] == nil {
				votes[g] = map[string]int{}
			}
			votes[g][cl]++
		}
	}
	for _, fr := range f.Frames {
		if len(fr.Bots) > 0 {
			r.Frames++
		}
		for _, b := range fr.Bots {
			party[b.G] = true
			if b.Cast != 0 {
				r.CastsSeen = true
				vote(b.G, spells.Name(b.Cast))
			}
			if b.Action != "" {
				r.ActsSeen = true
				vote(b.G, b.Action)
			}
			if b.Res != "" {
				r.VerdictSeen = true
			}
		}
	}
	classOf := map[string]string{}
	for g := range party {
		cl := ""
		for _, m := range f.Party {
			if m.G == g && m.Class != "" {
				cl = strings.ToLower(m.Class)
			}
		}
		if cl == "" {
			best, n := "", 0
			for k, v := range votes[g] {
				if v > n || (v == n && k < best) {
					best, n = k, v
				}
			}
			cl = best
		}
		if cl == "" {
			cl = "?"
		}
		classOf[g] = cl
	}
	if !r.CastsSeen && !r.ActsSeen {
		return r
	}

	// Pass 2: episodes per bot, the mix, idle runs, mob casts, mana tracks.
	st := map[string]*spellBot{}
	cls := map[string]*ClassSpell{}
	castN := map[string]map[string]int{}
	mixN := map[string]map[string]int{}
	whyN := map[string]map[string]int{}
	botsOf := map[string]map[string]bool{}
	get := func(cl string) *ClassSpell {
		if cls[cl] == nil {
			cls[cl] = &ClassSpell{Class: cl, Verdicts: map[string]int{}}
			castN[cl], mixN[cl], whyN[cl], botsOf[cl] = map[string]int{}, map[string]int{}, map[string]int{}, map[string]bool{}
		}
		return cls[cl]
	}
	var acts []actEv
	openMob := map[string]*mobCast{}
	var mcasts []*mobCast
	idleN := map[string]int{}
	engN := map[string]int{}
	manaUser := map[string]bool{}
	dry := map[string]bool{}
	var deaths []trace.Event // death events in time order, handled before the first frame at or after them
	for _, e := range f.Events {
		if e.Ev == "death" {
			deaths = append(deaths, e)
		}
	}
	sort.SliceStable(deaths, func(i, j int) bool { return deaths[i].T < deaths[j].T })
	nextDeath := 0
	mobAt := map[int64]map[string]trace.Mob{} // time -> mobs, for the cc check
	var frameTimes []int64
	endIdle := func(s *spellBot, cl string) {
		if len(s.idleRun) > 0 && float64(len(s.idleRun))*ivS >= idleRunSeconds {
			idleN[cl] += len(s.idleRun)
		}
		s.idleRun = nil
	}
	for _, fr := range f.Frames {
		for nextDeath < len(deaths) && deaths[nextDeath].T <= fr.T {
			r.Deaths = append(r.Deaths, deathMana(f, deaths[nextDeath], st, classOf))
			nextDeath++
		}
		mobs := map[string]trace.Mob{}
		partyTargeted := false
		for _, m := range fr.Mobs {
			mobs[m.G] = m
			if m.Combat && party[m.Target] && m.HPPct > 0 {
				partyTargeted = true
			}
		}
		mobAt[fr.T] = mobs
		frameTimes = append(frameTimes, fr.T)
		for _, b := range fr.Bots {
			cl := classOf[b.G]
			cs := get(cl)
			botsOf[cl][f.Name(b.G)] = true
			s := st[b.G]
			if s == nil {
				s = &spellBot{}
				st[b.G] = s
			}
			gap := fr.T-s.lastT > int64(3*interval) && s.seen
			// casts: a new cs after another or after none
			if b.Cast != 0 && (s.cs != b.Cast || gap) {
				name := spells.Name(b.Cast)
				if name == "" {
					name = fmt.Sprintf("spell %d", b.Cast)
				}
				castN[cl][name]++
				cs.Casts++
			}
			// actions: a change of (name, verdict)
			if b.Action != "" && (s.act != b.Action || s.res != b.Res || gap) {
				acts = append(acts, actEv{t: fr.T, g: b.G, name: b.Action, res: b.Res, target: b.Target, why: b.Why})
			}
			combat := b.Alive && (b.Combat || b.State == "c")
			if combat {
				cs.Frames++
				if b.Action != "" {
					cat, _ := CategoryOf(b.Action)
					mixN[cl][cat]++
				}
				if b.Res != "" {
					cs.Verdicts[b.Res]++
					if (b.Res == "F" || b.Res == "I") && b.Why != "" {
						whyN[cl][b.Why]++
					}
				}
				// idle: engaged with a hostile on the party, mana users only (the threshold is theirs)
				if b.PType == 0 && b.MaxPow > 0 {
					manaUser[b.G] = true
				}
				if partyTargeted && b.PType == 0 && b.MaxPow > 0 {
					engN[cl]++
					if r.VerdictSeen && b.Res != "O" && b.Cast == 0 {
						s.idleRun = append(s.idleRun, fr.T)
					} else {
						endIdle(s, cl)
					}
				} else {
					endIdle(s, cl)
				}
				// mana track of the fight
				if b.MaxPow > 0 {
					p := float64(b.Power) / float64(b.MaxPow) * 100
					if s.fight == nil || gap || s.wasDead {
						s.fight = &fightRun{start: p, min: p}
						s.wasDead = false
					}
					s.fight.min = min(s.fight.min, p)
					if b.PType == 0 && p < lowManaPct && !dry[b.G] {
						dry[b.G] = true
					}
				}
			} else {
				endIdle(s, cl)
				if b.Alive {
					s.fight = nil
				} else {
					s.wasDead = true
				}
			}
			s.cs, s.act, s.res, s.lastT, s.seen = b.Cast, b.Action, b.Res, fr.T, true
			if b.Alive {
				s.last = b
			}
		}
		// enemy casts
		seenNow := map[string]bool{}
		for _, m := range fr.Mobs {
			seenNow[m.G] = true
			o := openMob[m.G]
			switch {
			case m.Cast != 0 && o != nil && o.spell == m.Cast:
				o.last = fr.T
			case m.Cast != 0:
				if o != nil {
					o.closed = true
				}
				o = &mobCast{g: m.G, spell: m.Cast, first: fr.T, last: fr.T}
				openMob[m.G] = o
				mcasts = append(mcasts, o)
			default:
				if o != nil {
					o.closed = true
					delete(openMob, m.G)
				}
			}
		}
		for g, o := range openMob {
			if !seenNow[g] {
				o.closed = true
				delete(openMob, g)
			}
		}
	}
	for g, s := range st {
		endIdle(s, classOf[g])
	}

	for ; nextDeath < len(deaths); nextDeath++ {
		r.Deaths = append(r.Deaths, deathMana(f, deaths[nextDeath], st, classOf))
	}
	r.DryBots = len(dry)

	// classes
	var names []string
	for cl := range cls {
		names = append(names, cl)
	}
	sort.Strings(names)
	for _, cl := range names {
		c1 := cls[cl]
		for n := range botsOf[cl] {
			c1.Bots = append(c1.Bots, n)
		}
		sort.Strings(c1.Bots)
		c1.TopCasts = topN(castN[cl], 8)
		c1.Mix = topN(mixN[cl], 12)
		c1.Whys = topN(whyN[cl], 5)
		if len(c1.Verdicts) == 0 {
			c1.Verdicts = nil
		}
		r.Classes = append(r.Classes, *c1)
	}

	// idle share
	r.Idle.Recorded = r.VerdictSeen
	if r.VerdictSeen {
		r.Idle.ByClass = map[string]float64{}
		for cl, n := range engN {
			r.Idle.Engaged += n
			r.Idle.Idle += idleN[cl]
			if n > 0 {
				r.Idle.ByClass[cl] = round1(float64(idleN[cl]) * 100 / float64(n))
			}
		}
		if r.Idle.Engaged > 0 {
			r.Idle.Pct = round1(float64(r.Idle.Idle) * 100 / float64(r.Idle.Engaged))
		}
	}

	// interrupts, taunts, crowd control from the action events
	executed := func(a actEv) bool { return a.res == "O" || (!r.VerdictSeen && a.res == "") }
	intByBot := map[string]int{}
	var intr []actEv
	tauntBy := map[string]int{}
	var ccs []actEv
	ccBySpell := map[string]int{}
	for _, a := range acts {
		cat, single := CategoryOf(a.name)
		switch cat {
		case CatInterrupt:
			if executed(a) {
				intr = append(intr, a)
			}
		case CatTaunt:
			r.Taunts.Tried++
			if executed(a) {
				r.Taunts.Done++
				tauntBy[f.Name(a.g)]++
			}
		case CatCC:
			if single && executed(a) {
				ccs = append(ccs, a)
			}
		}
	}
	missed := map[string]int{}
	for _, mc := range mcasts {
		r.Interrupt.Casts++
		covered := false
		for _, a := range intr {
			if a.t >= mc.first-1000 && a.t <= mc.last+1000 {
				covered = true
				intByBot[f.Name(a.g)]++
				break
			}
		}
		name := spells.Name(mc.spell)
		if name == "" {
			name = fmt.Sprintf("spell %d", mc.spell)
		}
		if !covered {
			missed[name]++
			continue
		}
		r.Interrupt.Covered++
		if m, ok := spells.Lookup(mc.spell); ok && m.CastMS > 0 && mc.closed {
			seen := float64(mc.last-mc.first)/1000 + ivS
			if seen < cutShortOf*float64(m.CastMS)/1000 {
				r.Interrupt.CutShort++
			}
		}
	}
	if r.Interrupt.Casts > 0 {
		r.Interrupt.Pct = round1(float64(r.Interrupt.Covered) * 100 / float64(r.Interrupt.Casts))
	}
	r.Interrupt.ByBot = topN(intByBot, 5)
	r.Interrupt.TopMissed = topN(missed, 5)
	r.Taunts.ByBot = topN(tauntBy, 5)

	// crowd control: a mob shows the cc flag within ccWindowS of the action that it did not show before it. The action's target is not
	// the bot's own target (a polymorph goes to the marked mob), so any mob counts.
	for _, a := range ccs {
		r.CC.Casts++
		ccBySpell[normalizeAction(a.name)]++
		idx := sort.Search(len(frameTimes), func(i int) bool { return frameTimes[i] >= a.t })
		var before map[string]trace.Mob
		if idx > 0 {
			before = mobAt[frameTimes[idx-1]]
		}
		inView, landed := false, false
		for _, t := range frameTimes[idx:] {
			if float64(t-a.t)/1000 > ccWindowS {
				break
			}
			for g, m := range mobAt[t] {
				inView = true
				if m.CC && !before[g].CC {
					landed = true
				}
			}
			if landed {
				break
			}
		}
		if inView {
			r.CC.Evaluable++
			if landed {
				r.CC.Landed++
			}
		}
	}
	if r.CC.Evaluable > 0 {
		r.CC.Pct = round1(float64(r.CC.Landed) * 100 / float64(r.CC.Evaluable))
	}
	r.CC.BySpell = topN(ccBySpell, 5)

	r.warn(c)
	return r
}

func (r *SpellReport) warn(c Config) {
	if r.Idle.Recorded && r.Idle.Engaged >= idleMinFrames && r.Idle.Pct > c.IdleSharePct {
		r.Warnings = append(r.Warnings, fmt.Sprintf("idle_share %.1f %% of engaged mana-user frames (limit %g)", r.Idle.Pct, c.IdleSharePct))
	}
	if r.Interrupt.Casts >= minCastsRate && r.Interrupt.Pct < c.InterruptCoverPct {
		r.Warnings = append(r.Warnings, fmt.Sprintf("interrupt_cover %.1f %% of %d enemy casts met an interrupt (limit %g)", r.Interrupt.Pct, r.Interrupt.Casts, c.InterruptCoverPct))
	}
	if r.CC.Evaluable >= minCCRate && r.CC.Pct < c.CCSuccessPct {
		r.Warnings = append(r.Warnings, fmt.Sprintf("cc_success %.1f %% of %d crowd-control actions put a cc flag on a mob (limit %g)", r.CC.Pct, r.CC.Evaluable, c.CCSuccessPct))
	}
}

func topN(m map[string]int, n int) []NameCount {
	out := make([]NameCount, 0, len(m))
	for k, v := range m {
		out = append(out, NameCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func joinCounts(l []NameCount) string {
	var s []string
	for _, e := range l {
		s = append(s, fmt.Sprintf("%s %d", e.Name, e.N))
	}
	return strings.Join(s, ", ")
}

func pctOf(n, total int) string {
	if total == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", float64(n)*100/float64(total))
}

// Lines prints the report for a person: one block per topic, "not recorded" where the trace lacks the field.
func (r SpellReport) Lines() []string {
	if !r.CastsSeen && !r.ActsSeen {
		return []string{"Spells: not recorded (no bot frame has a cast or an action)"}
	}
	verdicts := "recorded"
	if !r.VerdictSeen {
		verdicts = "not recorded (older trace: executions are upper bounds)"
	}
	out := []string{fmt.Sprintf("Spells (report only; 1 Hz frames, so instants and short casts are not seen; action verdicts %s):", verdicts)}
	for _, c := range r.Classes {
		if c.Frames == 0 && c.Casts == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("  %-8s %s: %d casts seen over %d combat frames", c.Class, strings.Join(c.Bots, ", "), c.Casts, c.Frames))
		if len(c.TopCasts) > 0 {
			out = append(out, "           casts: "+joinCounts(c.TopCasts))
		}
		if len(c.Mix) > 0 {
			var parts []string
			total := 0
			for _, m := range c.Mix {
				total += m.N
			}
			for _, m := range c.Mix {
				if m.N*100 >= total { // under 1 % is left out
					parts = append(parts, fmt.Sprintf("%s %s", m.Name, pctOf(m.N, total)))
				}
			}
			out = append(out, "           actions: "+strings.Join(parts, ", "))
		}
		if len(c.Verdicts) > 0 {
			var parts []string
			total := 0
			for _, n := range c.Verdicts {
				total += n
			}
			for _, k := range []string{"O", "F", "I", "U", "P", "V"} {
				if c.Verdicts[k] > 0 {
					parts = append(parts, fmt.Sprintf("%s %s", k, pctOf(c.Verdicts[k], total)))
				}
			}
			line := "           verdicts: " + strings.Join(parts, ", ")
			if len(c.Whys) > 0 {
				line += "; why not: " + joinCounts(c.Whys)
			}
			out = append(out, line)
		}
	}
	if !r.Idle.Recorded {
		out = append(out, "  idle share: not recorded (no verdict letters in this trace)")
	} else {
		var parts []string
		var ks []string
		for k := range r.Idle.ByClass {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", k, r.Idle.ByClass[k]))
		}
		out = append(out, fmt.Sprintf("  idle share: %.1f %% of %d engaged mana-user frames had no executed action for %.0f s or more (%s)", r.Idle.Pct, r.Idle.Engaged, idleRunSeconds, strings.Join(parts, ", ")))
	}
	if len(r.Deaths) == 0 {
		out = append(out, "  power at death: no death")
	}
	for _, d := range r.Deaths {
		if !d.Seen {
			out = append(out, fmt.Sprintf("  power at death: %s died at %.0f s with no frame before it", d.Name, float64(d.AtMS)/1000))
			continue
		}
		out = append(out, fmt.Sprintf("  power at death: %s (%s) at %.0f s: %s %.0f %% (fight start %.0f %%, lowest %.0f %%), health %.0f %% in the last frame", d.Name, d.Class, float64(d.AtMS)/1000, d.Power, d.AtDeath, d.AtStart, d.Min, d.HPAtLast))
	}
	if r.DryBots > 0 {
		out = append(out, fmt.Sprintf("  dry: %d mana users went under %.0f %% mana in combat", r.DryBots, lowManaPct))
	}
	if r.Interrupt.Casts == 0 {
		out = append(out, "  interrupts: no enemy cast seen")
	} else {
		out = append(out, fmt.Sprintf("  interrupts: %d enemy casts seen, %d with an interrupt action within 1 s (%.0f %%), %d of those cut short; by %s; uncovered: %s",
			r.Interrupt.Casts, r.Interrupt.Covered, r.Interrupt.Pct, r.Interrupt.CutShort, orNone(joinCounts(r.Interrupt.ByBot)), orNone(joinCounts(r.Interrupt.TopMissed))))
	}
	tn := "recorded"
	if !r.VerdictSeen {
		tn = "verdict not recorded"
	}
	out = append(out, fmt.Sprintf("  taunts: %d executed of %d tried (%s); by %s", r.Taunts.Done, r.Taunts.Tried, tn, orNone(joinCounts(r.Taunts.ByBot))))
	if r.CC.Casts == 0 {
		out = append(out, "  crowd control: no single-target crowd-control action")
	} else {
		out = append(out, fmt.Sprintf("  crowd control: %d actions (%s), %d with mobs in view, %d put a new cc flag on a mob within %.0f s (%.0f %%)",
			r.CC.Casts, joinCounts(r.CC.BySpell), r.CC.Evaluable, r.CC.Landed, ccWindowS, r.CC.Pct))
	}
	for _, w := range r.Warnings {
		out = append(out, "  WARN "+w)
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// spellsCheck is the report as a check: always pass (n/a without casts or actions), the warnings in the detail and the count.
func spellsCheck(f *trace.File, c Config) trace.Check {
	const id, name = IDSpells, "Spell usage (report only)"
	r := Spells(f, c)
	if !r.CastsSeen && !r.ActsSeen {
		return na(id, name, "not recorded: no bot frame has a cast (cs) or an action (act)")
	}
	casts := 0
	for _, cl := range r.Classes {
		casts += cl.Casts
	}
	d := fmt.Sprintf("%d casts seen in %d classes; idle %s; interrupts %d/%d covered; taunts %d; cc %d/%d landed", casts, len(r.Classes), idleText(r), r.Interrupt.Covered, r.Interrupt.Casts, r.Taunts.Done, r.CC.Landed, r.CC.Evaluable)
	if !r.VerdictSeen {
		d += " (verdict letters not recorded)"
	}
	if len(r.Warnings) > 0 {
		d += "; WARN " + strings.Join(r.Warnings, "; ")
	}
	return trace.Check{ID: id, Name: name, Verdict: trace.Pass, Count: len(r.Warnings), Value: float64(len(r.Warnings)), Threshold: 0, Unit: "warnings", Detail: d}
}

func idleText(r SpellReport) string {
	if !r.Idle.Recorded {
		return "not recorded"
	}
	return fmt.Sprintf("%.0f%%", r.Idle.Pct)
}
