package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// createBlock appends one living create block (Object.cpp:274-308, :415-449) to w.
func createBlock(w *packet.Writer, g packet.GUID, typ uint8, x, y, z float32, vals map[uint16]uint32) {
	w.U8(uint8(objects.BlockCreate2)).PackedGUID(g).U8(typ)
	objtest.Living(w, 0x20, game.MoveInfo{X: x, Y: y, Z: z}, nil)
	objtest.Values(w, 192, vals)
}

// unitCreate is one SMSG_UPDATE_OBJECT holding a single living create block.
func unitCreate(g packet.GUID, typ uint8, x, y, z float32, vals map[uint16]uint32) []byte {
	w := objtest.Header(1)
	createBlock(w, g, typ, x, y, z, vals)
	return w.Bytes()
}

func valuesUpdate(g packet.GUID, vals map[uint16]uint32) []byte {
	w := objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(g)
	objtest.Values(w, 192, vals)
	return w.Bytes()
}

func TestUnitsWithNamesAndParty(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	h.send(opcode.SMSGUpdateObject, unitCreate(wolfGUID, objects.TypeUnit, me.X+10, me.Y, me.Z, map[uint16]uint32{objects.FieldEntry: 299, objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	if p := h.expect(opcode.CMSGCreatureQuery); packet.NewReader(p.Body).U32() != 299 {
		t.Fatalf("creature query % X", p.Body)
	}
	wolfName(h)
	h.send(opcode.SMSGCompressedUpdateObject, objtest.Compress(unitCreate(0x30, objects.TypePlayer, me.X+3, me.Y+4, me.Z, map[uint16]uint32{objects.UnitHealth: 500, objects.UnitMaxHealth: 600})))
	waitFor(t, func() bool {
		return len(h.s.Units(objects.Filter{})) == 2 && len(h.s.Units(objects.Filter{Name: "young wolf"})) == 1
	})
	us := h.s.Units(objects.Filter{})
	if us[0].Name != "Gorm" || !us[0].InParty || us[0].Distance != 5 || us[1].Name != "Young Wolf" || !us[1].Hostile || us[1].InParty {
		t.Fatalf("%+v", us)
	}
	h.logout()
}

func TestDeathAuraAndMoves(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	wolfName(h)
	h.send(opcode.SMSGUpdateObject, unitCreate(wolfGUID, objects.TypeUnit, 0, 0, 0, map[uint16]uint32{objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	h.send(opcode.SMSGUpdateObject, valuesUpdate(wolfGUID, map[uint16]uint32{objects.UnitAura + 32: 589}))
	h.send(opcode.SMSGUpdateObject, valuesUpdate(wolfGUID, map[uint16]uint32{objects.UnitHealth: 0, objects.UnitAura + 32: 0}))
	move := packet.NewWriter().PackedGUID(wolfGUID).F32(0).F32(0).F32(0).U32(1).U8(0).U32(0x100).U32(1000).U32(1).F32(7).F32(8).F32(9).Bytes()
	h.send(opcode.SMSGCompressedMoves, objtest.Compress(packet.NewWriter().U8(uint8(len(move)+2)).U16(opcode.SMSGMonsterMove).Append(move).Bytes()))
	h.send(opcode.SMSGUpdateObject, []byte{1, 0, 0, 0, 0, 9}) // unknown block type: dropped, logged
	h.logout()
	if d := ofKind(h.s.Log(), events.KindDeath); len(d) != 1 || d[0].Source != "Young Wolf" || d[0].SourceGUID != wolfGUID {
		t.Fatalf("death %+v", d)
	}
	a := ofKind(h.s.Log(), events.KindAuraApplied)
	r := ofKind(h.s.Log(), events.KindAuraRemoved)
	if len(a) != 1 || a[0].SpellID != 589 || a[0].Target != "Young Wolf" || a[0].Data["harmful"] != true || a[0].Data["slot"] != 32 ||
		len(r) != 1 || r[0].SpellID != 589 || r[0].Data["harmful"] != true || r[0].Seq < a[0].Seq {
		t.Fatalf("auras %+v %+v", a, r)
	}
	// The spline takes 1 s; once it has run, the wolf is at its end.
	h.s.Store().SetClock(func() time.Time { return time.Now().Add(2 * time.Second) })
	if u, ok := h.s.Store().Get(wolfGUID, 0, 0, 0); !ok || u.X != 7 || u.Z != 9 || u.Moving {
		t.Fatalf("monster move not applied: %+v", u)
	}
	if h.s.Status().ParseErrors != 1 {
		t.Fatalf("parse errors %d", h.s.Status().ParseErrors)
	}
}

// A positive slot (under 32) is a buff: aura_applied with harmful false.
func TestBuffIsNotHarmful(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	wolfName(h)
	h.send(opcode.SMSGUpdateObject, unitCreate(wolfGUID, objects.TypeUnit, 0, 0, 0, map[uint16]uint32{objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	h.send(opcode.SMSGUpdateObject, valuesUpdate(wolfGUID, map[uint16]uint32{objects.UnitAura + 3: 1243}))
	h.logout()
	if a := ofKind(h.s.Log(), events.KindAuraApplied); len(a) != 1 || a[0].Data["harmful"] != false || a[0].Data["slot"] != 3 {
		t.Fatalf("%+v", a)
	}
}

func TestUnitsBeforeWorldIsEmpty(t *testing.T) {
	h := start(t, Options{})
	if _, err := h.s.Unit(me.GUID, ""); !errors.Is(err, ErrNotInWorld) {
		t.Fatalf("unit before the world: %v", err)
	}
	if us := h.s.Units(objects.Filter{}); len(us) != 0 {
		t.Fatalf("%+v", us)
	}
	h.expect(opcode.CMSGCharEnum)
}

// The player's own create block feeds status (health, power) and stays out of units.
func TestStatusCarriesOwnHealthAndPower(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, unitCreate(me.GUID, objects.TypePlayer, me.X, me.Y, me.Z, map[uint16]uint32{
		objects.UnitHealth: 900, objects.UnitMaxHealth: 1000, objects.UnitBytes0: 1 << 24, // rage
		objects.UnitPower1 + 1: 30, objects.UnitMaxPower1 + 1: 100,
	}))
	waitFor(t, func() bool { return h.s.Status().Health == 900 })
	if st := h.s.Status(); st.MaxHealth != 1000 || st.Power != 30 || st.MaxPower != 100 {
		t.Fatalf("%+v", st)
	}
	if us := h.s.Units(objects.Filter{}); len(us) != 0 {
		t.Fatalf("the player is not a unit around itself: %+v", us)
	}
	h.logout()
}

// Ruling P7: the name filter sees past the 100 nearest units, and the caller's limit
// (100 when unset) applies after it.
func TestUnitsNameFilterAndLimit(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	wolfName(h)
	h.send(opcode.SMSGCreatureQueryResponse, creatureResponse(300, "Far Bear"))
	w := objtest.Header(121)
	for i := 0; i < 120; i++ {
		createBlock(w, packet.GUID(0xF13000012B000000|uint64(i+1)), objects.TypeUnit, me.X+float32(i+1), me.Y, me.Z,
			map[uint16]uint32{objects.UnitHealth: 1, objects.UnitMaxHealth: 1})
	}
	far := packet.GUID(0xF13000012C000001) // entry 300
	createBlock(w, far, objects.TypeUnit, me.X+500, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 1, objects.UnitMaxHealth: 1})
	h.send(opcode.SMSGUpdateObject, w.Bytes())
	waitFor(t, func() bool { return len(h.s.Units(objects.Filter{Limit: 1000})) == 121 })
	if us := h.s.Units(objects.Filter{Name: "BEAR"}); len(us) != 1 || us[0].GUID != far || us[0].Name != "Far Bear" {
		t.Fatalf("name filter: %+v", us)
	}
	if n := len(h.s.Units(objects.Filter{})); n != 100 {
		t.Fatalf("default limit: %d", n)
	}
	if us := h.s.Units(objects.Filter{Name: "wolf", Limit: 2}); len(us) != 2 || us[0].Distance != 1 || us[1].Distance != 2 {
		t.Fatalf("limit 2: %+v", us)
	}
	h.logout()
}

// Other players' movement relays (MovementHandler.cpp:414-418) move them; SMSG_DESTROY_OBJECT
// (Object.cpp:410-411) forgets them.
func TestMoveRelayAndDestroy(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, unitCreate(0x30, objects.TypePlayer, 1, 2, 3, map[uint16]uint32{objects.UnitHealth: 5, objects.UnitMaxHealth: 5}))
	h.expect(opcode.CMSGNameQuery)
	for _, op := range []uint16{opcode.MSGMoveHeartbeat, opcode.MSGMoveTeleport} {
		w := packet.NewWriter().PackedGUID(0x30)
		x := float32(op & 0xFF)
		game.WriteMoveInfo(w, game.MoveInfo{X: x, Y: 20, Z: 30, O: 1})
		h.send(op, w.Bytes())
		waitFor(t, func() bool { u, ok := h.s.Store().Get(0x30, 0, 0, 0); return ok && u.X == x && u.O == 1 })
	}
	h.send(opcode.SMSGDestroyObject, packet.NewWriter().GUID(0x30).Bytes())
	waitFor(t, func() bool { _, ok := h.s.Store().Get(0x30, 0, 0, 0); return !ok })
	h.send(opcode.MSGMoveHeartbeat, packet.NewWriter().PackedGUID(0x30).Bytes()) // truncated relay: a parse error
	h.logout()
	if n := h.s.Status().ParseErrors; n != 1 {
		t.Fatalf("parse errors %d", n)
	}
}

// Options.Store lets the caller share a store with the session.
func TestOptionsStoreIsUsed(t *testing.T) {
	st := objects.NewStore()
	h := start(t, Options{Store: st})
	if h.s.Store() != st {
		t.Fatal("store not used")
	}
	h.expect(opcode.CMSGCharEnum)
}

// A pet or guardian GUID (HIGHGUID_PET) carries a pet number in bits 24-47, not the entry
// (ObjectGuid.cpp:69-70): the live m4 run named a Voidwalker Minion (entry 8996, pet
// number 326) "creature 326". The creature query takes OBJECT_FIELD_ENTRY from the store,
// and a pet whose entry field is not known yet is not queried at all.
func TestPetNameUsesEntryField(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	wolfName(h)
	const minion = packet.GUID(0xF1400001460000D0) // pet number 0x146 = 326
	// The minion swings before its create block: the event waits instead of querying entry 326.
	h.send(opcode.SMSGAttackerstateupdate, gametest.AttackerStateUpdate(0, minion, wolfGUID, 12, 0, 0, 0, 1, 0))
	h.send(opcode.SMSGUpdateObject, unitCreate(minion, objects.TypeUnit, me.X+2, me.Y, me.Z,
		map[uint16]uint32{objects.FieldEntry: 8996, objects.UnitHealth: 538, objects.UnitMaxHealth: 538}))
	if p := h.expect(opcode.CMSGCreatureQuery); packet.NewReader(p.Body).U32() != 8996 {
		t.Fatalf("first creature query % X, want entry 8996", p.Body)
	}
	h.send(opcode.SMSGCreatureQueryResponse, creatureResponse(8996, "Voidwalker Minion"))
	waitFor(t, func() bool {
		us := h.s.Units(objects.Filter{})
		return len(us) == 1 && us[0].Name == "Voidwalker Minion"
	})
	if u := h.s.Units(objects.Filter{})[0]; u.Entry != 8996 || u.GUID != minion {
		t.Fatalf("%+v", u)
	}
	h.logout()
	if m := ofKind(h.s.Log(), events.KindMelee); len(m) != 1 || m[0].Source != "Voidwalker Minion" || m[0].Target != "Young Wolf" {
		t.Fatalf("melee %+v", m)
	}
}

// A unit's cast follows SMSG_SPELL_START until its SMSG_SPELL_GO or SMSG_SPELL_FAILED_OTHER;
// its stand state and auras follow the values updates; the player's own aura times follow
// SMSG_UPDATE_AURA_DURATION; Unit finds a unit by guid or name, the player included.
func TestUnitStateAndLookup(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	wolfName(h)
	h.send(opcode.SMSGUpdateObject, unitCreate(wolfGUID, objects.TypeUnit, me.X+5, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	// The player's own create block, somewhere it no longer is: Unit gives the session's position.
	h.send(opcode.SMSGUpdateObject, unitCreate(me.GUID, objects.TypePlayer, me.X-30, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 1, objects.UnitMaxHealth: 1}))
	h.send(opcode.SMSGSpellStart, gametest.SpellStart(wolfGUID, 116, 3000, me.GUID))
	casting := func(want uint32) func() bool {
		return func() bool {
			u, err := h.s.Unit(wolfGUID, "")
			return err == nil && ((want == 0 && u.Casting == nil) || (u.Casting != nil && u.Casting.SpellID == want))
		}
	}
	waitFor(t, casting(116))
	h.send(opcode.SMSGSpellGo, gametest.SpellGo(wolfGUID, 116, []packet.GUID{me.GUID}, nil, nil, me.GUID))
	waitFor(t, casting(0))
	h.send(opcode.SMSGSpellStart, gametest.SpellStart(wolfGUID, 133, 3000, me.GUID))
	waitFor(t, casting(133))
	h.send(opcode.SMSGSpellFailedOther, packet.NewWriter().GUID(wolfGUID).U32(133).Bytes())
	waitFor(t, casting(0))

	// The player sits down to eat; the server also says how long the food lasts.
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitBytes1: 1, objects.UnitAura + 2: 433}))
	h.send(opcode.SMSGUpdateAuraDuration, packet.NewWriter().U8(2).U32(30000).Bytes())
	waitFor(t, func() bool {
		u, err := h.s.Unit(0, strings.ToLower(me.Name))
		return err == nil && u.GUID == me.GUID && u.StandState == "sit" && len(u.Auras) == 1 && u.Auras[0].RemainingMS != nil
	})
	u, _ := h.s.Unit(me.GUID, "")
	if a := u.Auras[0]; a.Name != "Food" || a.Slot != 2 || *a.RemainingMS > 30000 || *a.RemainingMS < 25000 || u.Name != me.Name || u.Distance != 0 || u.X != me.X {
		t.Fatalf("%+v", u)
	}
	if u, err := h.s.Unit(0, "wolf"); err != nil || u.GUID != wolfGUID || u.Name != "Young Wolf" || u.Distance != 5 {
		t.Fatalf("by name: %+v %v", u, err)
	}
	if _, err := h.s.Unit(0, "bear"); err == nil {
		t.Fatal("no bear in view")
	}
	if _, err := h.s.Unit(0x99, ""); err == nil {
		t.Fatal("unknown guid")
	}
	if _, err := h.s.Unit(0, ""); err == nil {
		t.Fatal("neither guid nor name")
	}
	h.logout()
}

// ServerSelection is the target field of the player's own object: it follows the server's updates, not what the session sent.
func TestServerSelectionFollowsTheServersUpdates(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	if _, ok := h.s.ServerSelection(); ok {
		t.Fatal("a selection before the player's own object is known")
	}
	h.send(opcode.SMSGUpdateObject, unitCreate(me.GUID, objects.TypePlayer, me.X, me.Y, me.Z, map[uint16]uint32{
		objects.UnitHealth: 900, objects.UnitMaxHealth: 1000, objects.UnitTarget: 0x2A, objects.UnitTarget + 1: 0}))
	waitFor(t, func() bool { g, ok := h.s.ServerSelection(); return ok && g == 0x2A })
	// The session selecting another unit changes what it believes, not what the server shows, until the server's update comes.
	if _, err := h.s.Target(context.Background(), 0x99, ""); err != nil {
		t.Fatal(err)
	}
	if g, _ := h.s.ServerSelection(); g != 0x2A || h.s.Status().Target != 0x99 {
		t.Fatalf("server %v client %v", g, h.s.Status().Target)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitTarget: 0x99, objects.UnitTarget + 1: 0}))
	waitFor(t, func() bool { g, _ := h.s.ServerSelection(); return g == 0x99 })
	h.logout()
}
