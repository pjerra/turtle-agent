package dungeon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The gossip step (Gossip in plan.go): the agent's own character talks to an NPC. The remote driver also lets a bot do it (remote_gossip.go);
// this file is what both drivers share: the NPC's lookup in the agent's object store, the option match, and the talk itself (hello, find the
// option, select, wait for the close), with its retry for a menu that lacks the option.

// The kinds of answer a gossip hello or select gets.
const (
	GossipAnswerMenu     = "menu"     // SMSG_GOSSIP_MESSAGE: TextID and Options
	GossipAnswerComplete = "complete" // SMSG_GOSSIP_COMPLETE: the conversation is closed
	GossipAnswerOther    = "other"    // another panel (a vendor or trainer list, a quest panel): Other names its event
	GossipAnswerNone     = "none"     // nothing came in the wait: the NPC is out of reach or cannot talk
)

// GossipLine is one option line of a menu.
type GossipLine struct {
	Index uint32
	Text  string
	Coded bool // the client asks for a code first: a plan cannot pick it
}

// GossipAnswer is how the server answered a hello or a select.
type GossipAnswer struct {
	Kind    string
	TextID  uint32
	Options []GossipLine
	Other   string
}

// GossipTalker is the optional part of a game that lets the agent's own character talk to an NPC: CMSG_GOSSIP_HELLO and
// CMSG_GOSSIP_SELECT_OPTION, each with the wait for the server's answer.
type GossipTalker interface {
	// GossipHello talks to the NPC and waits up to wait for the menu (or the close, or another panel).
	GossipHello(ctx context.Context, npc packet.GUID, wait time.Duration) (GossipAnswer, error)
	// GossipSelect picks the option of the open menu and waits up to wait for the answer.
	GossipSelect(ctx context.Context, npc packet.GUID, option uint32, wait time.Duration) (GossipAnswer, error)
}

// Waits and distances of the step.
const (
	gossipHelloWait   = 3 * time.Second
	gossipSelectWait  = 5 * time.Second
	gossipPoll        = 2 * time.Second // between two hellos of a menu that lacks the option
	gossipStandoff    = 2.5             // yards short of the NPC the agent stops when the step names no stand
	gossipReach       = 4.0             // yards: the agent walks when it stands farther from the NPC (the probe's hello worked at 3.6, not at 9)
	gossipFindRadius  = 15.0            // yards around Near the NPC is looked for
	gossipViewLooks   = 3               // looks (a poll apart) an NPC gets to show up in the object store after the approach
	gossipMenuListMax = 6               // options listed in a failure
)

// gossipStatus says what the agent's own view shows of the NPC.
type gossipStatus int

const (
	gossipSeen    gossipStatus = iota // a living NPC of the entry within gossipFindRadius of Near
	gossipMissing                     // none in view
	gossipDead                        // only a corpse
)

// findGossipNPC is the NPC of the step in the agent's object store: the living creature of its entry nearest to Near within
// gossipFindRadius, else (when the store has only a corpse of it) the nearest corpse, which the caller fails on.
func findGossipNPC(units []objects.Unit, g *Gossip) (objects.Unit, gossipStatus) {
	var best, corpse *objects.Unit
	var bd, cd float64
	for i := range units {
		u := &units[i]
		if u.Type != "creature" || u.Entry != g.NPC || u.PosAgeMS < 0 {
			continue
		}
		d := dist3(Point{u.X, u.Y, u.Z}, g.Near)
		if d > gossipFindRadius {
			continue
		}
		if u.Dead || (u.MaxHealth > 0 && u.Health == 0) {
			if corpse == nil || d < cd {
				corpse, cd = u, d
			}
			continue
		}
		if best == nil || d < bd {
			best, bd = u, d
		}
	}
	switch {
	case best != nil:
		return *best, gossipSeen
	case corpse != nil:
		return *corpse, gossipDead
	}
	return objects.Unit{}, gossipMissing
}

// npcName is how a failure names the NPC.
func (g *Gossip) npcName(u objects.Unit) string {
	if u.Name != "" {
		return u.Name
	}
	return fmt.Sprintf("NPC %d", g.NPC)
}

// pick is the option of the menu the step names: its index, or the line that holds Text (ignoring case). A text that two lines hold is
// no pick (the plan has to say which); ok is false and why says so.
func (g *Gossip) pick(opts []GossipLine) (line GossipLine, ok bool, why string) {
	if g.Option != nil {
		for _, o := range opts {
			if o.Index == *g.Option {
				return o, true, ""
			}
		}
		return GossipLine{}, false, ""
	}
	want := strings.ToLower(g.Text)
	var hit []GossipLine
	for _, o := range opts {
		if strings.Contains(strings.ToLower(o.Text), want) {
			hit = append(hit, o)
		}
	}
	switch len(hit) {
	case 0:
		return GossipLine{}, false, ""
	case 1:
		return hit[0], true, ""
	}
	return GossipLine{}, false, fmt.Sprintf("the text %q matches %d options (%s): the plan has to name one", g.Text, len(hit), listOptions(hit))
}

// listOptions is the options of a menu for a message: `1 "Talk"`, ...; "no options" for none.
func listOptions(opts []GossipLine) string {
	if len(opts) == 0 {
		return "no options"
	}
	var parts []string
	for i, o := range opts {
		if i == gossipMenuListMax {
			parts = append(parts, fmt.Sprintf("and %d more", len(opts)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("%d %q", o.Index, o.Text))
	}
	return strings.Join(parts, ", ")
}

// gossipTalk is one talk of the agent's own character: the world it reads (the NPC in view, the time) and what it does (the sleep that keeps
// the run alive, the notes).
type gossipTalk struct {
	g     *Gossip
	tk    GossipTalker
	now   func() time.Time
	pause func(ctx context.Context, d time.Duration) error
	// npc is the NPC in the agent's view now, and how far the agent stands from it (-1: unknown).
	npc  func() (objects.Unit, gossipStatus)
	note func(string)
	// attempts is how many hellos were sent.
	attempts int
	// menu is the id of the last menu seen (0: none).
	menu uint32
}

// run talks to the NPC until the option is picked and the conversation closed. A menu without the option, an answer of nothing or a hello
// that closes at once are asked again every gossipPoll up to the step's retry_s; a missing or dead NPC, an option that two lines match, a
// coded one, another panel, or a select that is not answered with the close, fails the step at once.
func (t *gossipTalk) run(ctx context.Context) error {
	g := t.g
	until := t.now().Add(g.Retry())
	var last string // why the last hello did not do
	for {
		u, st := t.npc()
		switch st {
		case gossipMissing:
			return fmt.Errorf("NPC %d is not in view within %.0f yd of %.1f %.1f %.1f: it is missing", g.NPC, gossipFindRadius, g.Near.X, g.Near.Y, g.Near.Z)
		case gossipDead:
			return fmt.Errorf("%s (NPC %d) is dead: it cannot talk", g.npcName(u), g.NPC)
		}
		name := g.npcName(u)
		t.attempts++
		ans, err := t.tk.GossipHello(ctx, u.GUID, gossipHelloWait)
		if err != nil {
			return fmt.Errorf("talking to %s: %w", name, err)
		}
		switch ans.Kind {
		case GossipAnswerMenu:
			t.menu = ans.TextID
			line, ok, why := g.pick(ans.Options)
			if why != "" {
				return fmt.Errorf("%s shows menu %d: %s", name, ans.TextID, why)
			}
			if ok {
				return t.selectOption(ctx, u, name, line)
			}
			last = g.menuFailure(name, ans)
		case GossipAnswerComplete:
			last = fmt.Sprintf("%s closed the conversation at once", name)
		case GossipAnswerOther:
			return fmt.Errorf("%s answered with a %s panel, not a gossip menu", name, ans.Other)
		default:
			last = fmt.Sprintf("%s did not answer the hello (the agent stands %.1f yd from it; the server answers only within about %.0f)", name, u.Distance, GossipRange)
		}
		if !t.now().Before(until) {
			return fmt.Errorf("%s (after %.0f s, %d tries)", last, g.Retry().Seconds(), t.attempts)
		}
		if err := t.pause(ctx, gossipPoll); err != nil {
			return err
		}
	}
}

// menuFailure is the failure of a menu that lacks the option: "Weegli shows menu 1513: the event is not over" with the plan's fail_text,
// else what the menu offers.
func (g *Gossip) menuFailure(name string, ans GossipAnswer) string {
	if g.FailText != "" {
		return fmt.Sprintf("%s shows menu %d: %s", name, ans.TextID, g.FailText)
	}
	return fmt.Sprintf("%s shows menu %d, which lacks %s (it offers: %s)", name, ans.TextID, g.OptionText(), listOptions(ans.Options))
}

func (t *gossipTalk) selectOption(ctx context.Context, u objects.Unit, name string, line GossipLine) error {
	if line.Coded {
		return fmt.Errorf("%s: option %d (%q) asks for a code: a gossip step cannot pick it", name, line.Index, line.Text)
	}
	ans, err := t.tk.GossipSelect(ctx, u.GUID, line.Index, gossipSelectWait)
	if err != nil {
		return fmt.Errorf("picking option %d (%q) of %s: %w", line.Index, line.Text, name, err)
	}
	switch ans.Kind {
	case GossipAnswerComplete:
		t.note(fmt.Sprintf("gossip: %s: option %d (%q) of menu %d picked; the conversation closed", name, line.Index, line.Text, t.menu))
		return nil
	case GossipAnswerMenu:
		return fmt.Errorf("%s answered option %d (%q) with another menu (%d), not a close: the plan has no step for a second menu", name, line.Index, line.Text, ans.TextID)
	case GossipAnswerOther:
		return fmt.Errorf("%s answered option %d (%q) with a %s panel, not a close", name, line.Index, line.Text, ans.Other)
	}
	return fmt.Errorf("%s sent no close within %.0f s of option %d (%q): the option was sent, but nothing says it ran", name, gossipSelectWait.Seconds(), line.Index, line.Text)
}

// gossipSpot is where the agent should stand for the step: the plan's stand, else gossipStandoff yards short of the NPC along the way from
// `from` (the straight line when there is no planner); ok is false when the agent already stands within gossipReach of the NPC (and, with a
// stand, at it).
func gossipSpot(g *Gossip, from, npc Point, path func(from, to Point) []Point) (Point, bool) {
	if g.Stand != nil {
		if dist3(from, *g.Stand) <= ArriveRadius {
			return Point{}, false
		}
		return *g.Stand, true
	}
	if dist3(from, npc) <= gossipReach {
		return Point{}, false
	}
	pts := []Point{from, npc}
	if path != nil {
		if p := path(from, npc); len(p) >= 2 {
			pts = p
		}
	}
	if sp, ok := StandoffPoint(pts, gossipStandoff); ok {
		return sp, true
	}
	return npc, true
}

// gossipSettle is how many looks a step lets the NPC show up in the object store after the approach (objects load as the player nears).
func gossipSettle(ctx context.Context, looks int, pause func(context.Context, time.Duration) error, find func() gossipStatus) error {
	for i := 0; i < looks && find() == gossipMissing; i++ {
		if err := pause(ctx, gossipPoll/2); err != nil {
			return err
		}
	}
	return nil
}
