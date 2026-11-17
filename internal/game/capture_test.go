package game

import (
	"math"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/testfixture"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

// fixture loads a capture recorded on m910q by a milestone's live step (ruling P11:
// the shared testfixture.Load does the work).
func fixture(t *testing.T, name string) []capture.Record {
	t.Helper()
	return testfixture.Load(t, name)
}

// Every chat packet the server sent during milestone 2 parses, and the say, .bot, GM and
// player-not-found replies are there.
func TestM2ChatFromCapture(t *testing.T) {
	recs := fixture(t, "m2-chat.twcap")
	var sawVersion, sawSay, sawBotList bool
	for _, body := range capture.Bodies(recs, opcode.SMSGMessagechat) {
		m, err := ParseMessageChat(body)
		if err != nil {
			t.Fatalf("% X: %v", body, err)
		}
		sawVersion = sawVersion || (m.Type == ChatSystem && strings.HasPrefix(m.Text, "TBM:VERSION|"))
		sawSay = sawSay || (m.Type == ChatSay && m.Text == "turtle-agent milestone 2")
		sawBotList = sawBotList || (m.Type == ChatSystem && strings.HasPrefix(m.Text, "No owned PlayerBots"))
	}
	if !sawVersion || !sawSay || !sawBotList {
		t.Fatalf("version line %v, say line %v, .bot list reply %v", sawVersion, sawSay, sawBotList)
	}
	// The server answers .gm off and .gm visible on with SMSG_NOTIFICATION, not a system chat line.
	notes := map[string]bool{}
	for _, body := range capture.Bodies(recs, opcode.SMSGNotification) {
		text, err := ParseCString(body)
		if err != nil {
			t.Fatalf("% X: %v", body, err)
		}
		notes[text] = true
	}
	for _, want := range []string{"GM mode is OFF", "You are now visible."} {
		if !notes[want] {
			t.Errorf("no notification %q", want)
		}
	}
	notFound := capture.Bodies(recs, opcode.SMSGChatPlayerNotFound)
	if len(notFound) == 0 {
		t.Fatal("no SMSG_CHAT_PLAYER_NOT_FOUND captured")
	}
	for _, body := range notFound {
		if name, err := ParseCString(body); err != nil || name != "Zqxnobodyx" {
			t.Fatalf("player not found % X: %q, %v", body, name, err)
		}
	}
}

// Every group packet from milestone 3 parses; the populated list carries the bots.
func TestM3PartyFromCapture(t *testing.T) {
	recs := fixture(t, "m3-party.twcap")
	most := 0
	for _, body := range capture.Bodies(recs, opcode.SMSGGroupList) {
		g, err := ParseGroupList(body)
		if err != nil {
			t.Fatalf("% X: %v", body, err)
		}
		if len(g.Members) > most {
			most = len(g.Members)
		}
		if len(g.Members) > 0 && g.Leader == 0 {
			t.Fatalf("a populated group list without a leader: %+v", g)
		}
	}
	if most < 2 {
		t.Fatalf("largest group list had %d members, want 2", most)
	}
	stats := append(capture.Bodies(recs, opcode.SMSGPartyMemberStats), capture.Bodies(recs, opcode.SMSGPartyMemberStatsFull)...)
	if len(stats) == 0 {
		t.Fatal("no member stats captured")
	}
	full := false
	for _, body := range stats {
		m, err := ParsePartyMemberStats(body)
		if err != nil {
			t.Fatalf("% X: %v", body, err)
		}
		if m.MaxHP > 0 && m.Level > 0 {
			full = true
		}
	}
	if !full {
		t.Fatal("no member stats carried both MaxHP and Level")
	}
}

// Milestone 5: the attack and teleport packets the server sent parse with the source layouts.
func TestM5ActionsFromCapture(t *testing.T) {
	recs := fixture(t, "m5-attack.twcap")
	starts := capture.Bodies(recs, opcode.SMSGAttackstart)
	if len(starts) == 0 {
		t.Fatal("no SMSG_ATTACKSTART captured")
	}
	for _, b := range starts {
		if a, err := ParseAttackStart(b); err != nil || a.Attacker == 0 || a.Victim == 0 {
			t.Fatalf("% X: %+v %v", b, a, err)
		}
	}
	for _, b := range capture.Bodies(recs, opcode.SMSGAttackstop) {
		if _, err := ParseAttackStop(b); err != nil {
			t.Fatalf("% X: %v", b, err)
		}
	}
	teleports := 0
	for _, b := range capture.Bodies(recs, opcode.MSGMoveTeleportAck) {
		if _, err := ParseTeleportAck(b); err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		teleports++
	}
	for _, b := range capture.Bodies(recs, opcode.SMSGNewWorld) {
		if _, err := ParseNewWorld(b); err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		teleports++
	}
	if teleports == 0 {
		t.Fatal("no teleport captured")
	}
}

// m5Player is Tagentone's GUID in m5-attack.twcap (the "in world" line of the Task 25 run).
const m5Player packet.GUID = 0x1FA

// clientBodies is capture.Bodies for the packets the agent sent (Bodies keeps only the server's).
func clientBodies(recs []capture.Record, op uint16) [][]byte {
	var out [][]byte
	for _, r := range recs {
		if r.Op == op && r.Dir == worldconn.ClientToServer && !r.Redacted {
			out = append(out, r.Body)
		}
	}
	return out
}

// Milestone 5 content: the player swung at the giraffe and hit it, Heroic Strike (78) went out and
// came back refused for the missing weapon (reason 25, SPELL_FAILED_EQUIPPED_ITEM_CLASS), and every
// GM teleport was acked with the server's counter, ending at the requested point.
func TestM5ContentFromCapture(t *testing.T) {
	recs := fixture(t, "m5-attack.twcap")

	swing := false
	for _, b := range capture.Bodies(recs, opcode.SMSGAttackstart) {
		a, err := ParseAttackStart(b)
		if err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		swing = swing || (a.Attacker == m5Player && a.Victim != 0 && a.Victim != m5Player)
	}
	if !swing {
		t.Error("no SMSG_ATTACKSTART from the player on a unit")
	}

	hits := 0
	for _, b := range capture.Bodies(recs, opcode.SMSGAttackerstateupdate) {
		m, err := ParseAttackerStateUpdate(b)
		if err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		if m.Attacker == m5Player && m.Victim != 0 && m.Total > 0 {
			hits++
		}
	}
	if hits == 0 {
		t.Error("no SMSG_ATTACKERSTATEUPDATE from the player with damage")
	}

	const heroicStrike, equippedItemClass = 78, 25
	casts := 0
	for _, b := range clientBodies(recs, opcode.CMSGCastSpell) {
		r := packet.NewReader(b)
		if spell := r.U32(); r.Err() == nil && spell == heroicStrike {
			casts++
		}
	}
	if casts == 0 {
		t.Error("no CMSG_CAST_SPELL for spell 78")
	}
	refused := 0
	for _, b := range capture.Bodies(recs, opcode.SMSGCastResult) {
		c, err := ParseCastResult(b)
		if err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		if c.SpellID == heroicStrike && c.Failed && c.Reason == equippedItemClass {
			refused++
		}
	}
	if refused == 0 {
		t.Error("no SMSG_CAST_RESULT refusing spell 78 with reason 25")
	}

	// The server numbers its teleports 0, 1, 2, …; the client acks each with the same counter.
	acked := map[uint32]bool{}
	for _, b := range clientBodies(recs, opcode.MSGMoveTeleportAck) {
		r := packet.NewReader(b)
		g, counter := r.GUID(), r.U32()
		if r.Err() == nil && g == m5Player {
			acked[counter] = true
		}
	}
	var acks []TeleportAck
	for _, b := range capture.Bodies(recs, opcode.MSGMoveTeleportAck) {
		a, err := ParseTeleportAck(b)
		if err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		acks = append(acks, a)
	}
	if len(acks) == 0 {
		t.Fatal("no MSG_MOVE_TELEPORT_ACK from the server")
	}
	for i, a := range acks {
		if a.GUID != m5Player || a.Counter != uint32(i) || !acked[a.Counter] {
			t.Errorf("teleport %d: guid %v counter %d, client acked %v", i, a.GUID, a.Counter, acked[a.Counter])
		}
	}
	// The last teleport is the ground-level Barrens spot (-1287, -2379.6, 91.7).
	last := acks[len(acks)-1].Move
	if math.Abs(float64(last.X+1287)) > 0.1 || math.Abs(float64(last.Y+2379.6)) > 0.1 || math.Abs(float64(last.Z-91.7)) > 0.1 {
		t.Errorf("last teleport at (%v, %v, %v), want (-1287, -2379.6, 91.7)", last.X, last.Y, last.Z)
	}
}

// Every combat packet the server sent in milestones 5 and 6 parses with the source layouts.
func TestCombatFromCaptures(t *testing.T) {
	parsers := map[uint16]func([]byte) error{
		opcode.SMSGAttackerstateupdate:    func(b []byte) error { _, err := ParseAttackerStateUpdate(b); return err },
		opcode.SMSGSpellnonmeleedamagelog: func(b []byte) error { _, err := ParseSpellNonMeleeDamageLog(b); return err },
		opcode.SMSGPeriodicauralog:        func(b []byte) error { _, err := ParsePeriodicAuraLog(b); return err },
		opcode.SMSGSpellheallog:           func(b []byte) error { _, err := ParseSpellHealLog(b); return err },
		opcode.SMSGSpellenergizelog:       func(b []byte) error { _, err := ParseSpellEnergizeLog(b); return err },
		opcode.SMSGSpellStart:             func(b []byte) error { _, err := ParseSpellStart(b); return err },
		opcode.SMSGSpellGo:                func(b []byte) error { _, err := ParseSpellGo(b); return err },
		opcode.SMSGCastResult:             func(b []byte) error { _, err := ParseCastResult(b); return err },
		opcode.SMSGSpellFailedOther:       func(b []byte) error { _, err := ParseSpellFailedOther(b); return err },
		opcode.SMSGSpelllogmiss:           func(b []byte) error { _, err := ParseSpellLogMiss(b); return err },
		opcode.SMSGPartykilllog:           func(b []byte) error { _, err := ParsePartyKillLog(b); return err },
	}
	seen := map[uint16]int{}
	for _, name := range []string{"m5-attack.twcap", "m6-fight.twcap"} {
		recs := fixture(t, name)
		for op, parse := range parsers {
			for _, body := range capture.Bodies(recs, op) {
				if err := parse(body); err != nil {
					t.Fatalf("%s %s % X: %v", name, opcode.Name(op), body, err)
				}
				seen[op]++
			}
		}
	}
	if seen[opcode.SMSGAttackerstateupdate] == 0 || seen[opcode.SMSGSpellGo] == 0 {
		t.Fatalf("fixtures lack melee or spell packets: %v", seen)
	}
}

// Milestone 6 content (the headless MVP fight, Task 30): the player and its two hired bots
// fought the Barrens Giraffe. The player hit it in melee, each bot dealt damage or healing,
// the party killed it, and every bot that acted is in the group list, which is what the
// session's party flag on their events rests on (Session.appendEvent). The test spell,
// Quel'dorei Meditation (52524, no weapon needed), went off and ticked rage.
func TestM6FightContentFromCapture(t *testing.T) {
	recs := fixture(t, "m6-fight.twcap")
	const player packet.GUID = 0x1FA // Tagentone, as in m5-attack.twcap
	const meditation = 52524

	group := map[packet.GUID]string{}
	for _, b := range capture.Bodies(recs, opcode.SMSGGroupList) {
		g, err := ParseGroupList(b)
		if err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		for _, m := range g.Members {
			group[m.GUID] = m.Name
		}
	}
	if len(group) < 2 {
		t.Fatalf("group lists name %d members, want the 2 bots: %v", len(group), group)
	}

	var mob packet.GUID
	for _, b := range capture.Bodies(recs, opcode.SMSGAttackstart) {
		if a, err := ParseAttackStart(b); err == nil && a.Attacker == player {
			mob = a.Victim
		}
	}
	if !mob.IsCreature() {
		t.Fatalf("player's attack target %v is not a creature", mob)
	}

	playerMelee := 0
	acted := map[packet.GUID]uint32{} // damage or healing per non-player source
	for _, b := range capture.Bodies(recs, opcode.SMSGAttackerstateupdate) {
		m, _ := ParseAttackerStateUpdate(b)
		if m.Attacker == player && m.Victim == mob && m.Total > 0 {
			playerMelee++
		}
		if m.Attacker != player && m.Victim == mob {
			acted[m.Attacker] += m.Total
		}
	}
	for _, b := range capture.Bodies(recs, opcode.SMSGSpellnonmeleedamagelog) {
		d, _ := ParseSpellNonMeleeDamageLog(b)
		if d.Target == mob {
			acted[d.Attacker] += d.Damage
		}
	}
	for _, b := range capture.Bodies(recs, opcode.SMSGSpellheallog) {
		h, _ := ParseSpellHealLog(b)
		acted[h.Caster] += h.Amount
	}
	for _, b := range capture.Bodies(recs, opcode.SMSGPeriodicauralog) {
		p, _ := ParsePeriodicAuraLog(b)
		for _, tick := range p.Ticks {
			if tick.Kind() == "heal" {
				acted[p.Caster] += tick.Amount
			}
		}
	}
	if playerMelee == 0 {
		t.Error("no melee damage from the player on the mob")
	}
	bots := 0
	for g, amount := range acted {
		if g == player || g == mob || amount == 0 {
			continue
		}
		if _, ok := group[g]; !ok {
			t.Errorf("%v dealt damage or healing but is not in any group list", g)
			continue
		}
		bots++
	}
	if bots < 2 {
		t.Errorf("%d bots dealt damage or healing, want both: %v (group %v)", bots, acted, group)
	}

	killed := false
	for _, b := range capture.Bodies(recs, opcode.SMSGPartykilllog) {
		k, err := ParsePartyKillLog(b)
		if err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		_, inGroup := group[k.Killer]
		killed = killed || (k.Victim == mob && (k.Killer == player || inGroup))
	}
	if !killed {
		t.Error("no SMSG_PARTYKILLLOG for the mob by the party")
	}

	casts, ticks := 0, 0
	for _, b := range capture.Bodies(recs, opcode.SMSGSpellGo) {
		if g, _ := ParseSpellGo(b); g.Caster == player && g.SpellID == meditation {
			casts++
		}
	}
	for _, b := range capture.Bodies(recs, opcode.SMSGPeriodicauralog) {
		p, _ := ParsePeriodicAuraLog(b)
		for _, tick := range p.Ticks {
			if p.Caster == player && p.SpellID == meditation && tick.Kind() == "energize" && tick.Amount > 0 {
				ticks++
			}
		}
	}
	if casts == 0 || ticks == 0 {
		t.Errorf("test spell %d: %d SMSG_SPELL_GO from the player, %d energize ticks", meditation, casts, ticks)
	}
}
