package objects

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/testfixture"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

const (
	door   packet.GUID = 0xF11001E5D3003839 // entry 124371, the Keystone door of Uldaman
	chest  packet.GUID = 0xF110000E7B008203 // entry 3707, a Food Crate
	locked packet.GUID = 0xF11002038A009EF4 // entry 131978, a locked chest
)

func goBlock(g packet.GUID, x, y, z float32, vals map[uint16]uint32) Block {
	return Block{Type: BlockCreate2, GUID: g, ObjType: TypeGameObject, Move: &Movement{HasPosition: true, X: x, Y: y, Z: z}, Values: vals}
}

func TestGameObjectsListFilterAndSort(t *testing.T) {
	s := NewStore()
	ch := s.Apply([]Block{
		goBlock(door, 10, 0, 0, map[uint16]uint32{FieldEntry: 124371, GOTypeID: uint32(GOTypeDoor), GOFlags: 32, GOState: GOStateReady, GODisplayID: 1369}),
		goBlock(chest, 3, 4, 0, map[uint16]uint32{FieldEntry: 3707, GOTypeID: 3, GODynFlags: 1}),
		goBlock(locked, 100, 0, 0, map[uint16]uint32{FieldEntry: 131978, GOTypeID: 3, GOFlags: GOFlagLocked, GODynFlags: 1}),
		createBlock(wolf, TypeUnit, 1, 1, map[uint16]uint32{FieldEntry: 299, UnitHealth: 10, UnitMaxHealth: 10}),
	})
	if len(ch) != 4 {
		t.Fatalf("creation reports only ChangeCreated: %+v", ch)
	}
	all := s.GameObjects(GOFilter{}, 0, 0, 0)
	if len(all) != 3 || all[0].GUID != chest || all[1].GUID != door || all[2].GUID != locked {
		t.Fatalf("nearest first, units excluded: %+v", all)
	}
	if len(s.Units(Filter{}, 0, 0, 0)) != 1 {
		t.Fatal("a game object is no unit")
	}
	c, d, l := all[0], all[1], all[2]
	if c.Distance != 5 || c.Entry != 3707 || c.Type != 3 || c.TypeName != "chest" || c.State != "active" || !c.Usable || c.Locked || c.DynFlags != 1 {
		t.Fatalf("chest %+v", c) // an absent state field is 0: GO_STATE_ACTIVE, as the client reads it
	}
	if d.TypeName != "door" || d.State != "ready" || d.Flags != 32 || !d.Usable || d.DisplayID != 1369 {
		t.Fatalf("door %+v", d)
	}
	if !l.Locked || !l.Usable {
		t.Fatalf("locked chest %+v", l)
	}

	if got := s.GameObjects(GOFilter{Near: 20}, 0, 0, 0); len(got) != 2 {
		t.Fatalf("within 20 yards: %+v", got)
	}
	if got := s.GameObjects(GOFilter{Entry: 124371}, 0, 0, 0); len(got) != 1 || got[0].GUID != door {
		t.Fatalf("by entry: %+v", got)
	}
	typ := GOTypeDoor
	if got := s.GameObjects(GOFilter{Type: &typ}, 0, 0, 0); len(got) != 1 || got[0].GUID != door {
		t.Fatalf("by type door (0 is a type, not an unset filter): %+v", got)
	}
	if got := s.GameObjects(GOFilter{Limit: 1}, 0, 0, 0); len(got) != 1 || got[0].GUID != chest {
		t.Fatalf("limit: %+v", got)
	}
	if g, ok := s.GameObject(door, 0, 0, 0); !ok || g.Entry != 124371 {
		t.Fatalf("by guid: %+v %v", g, ok)
	}
	if _, ok := s.GameObject(wolf, 0, 0, 0); ok {
		t.Fatal("a unit came back as a game object")
	}
	if !s.IsGameObject(door) || s.IsGameObject(wolf) || s.IsGameObject(0x99) {
		t.Fatal("IsGameObject")
	}
}

// Game object fields reuse the unit indices: GAMEOBJECT_LEVEL is where a unit's health sits and
// GAMEOBJECT_STATE where its created-by guid does. None of that may read as a unit change.
func TestGameObjectChangesAndNoUnitConfusion(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{goBlock(door, 1, 1, 1, map[uint16]uint32{FieldEntry: 124371, GOTypeID: 0, GOFlags: 32, GOState: GOStateReady})})
	ch := s.Apply([]Block{{Type: BlockValues, GUID: door, Values: map[uint16]uint32{
		GOState: GOStateActive, GOFlags: 5, GODynFlags: 1, UnitAura + 1: 7, GOLevel: 9}}}) // GOLevel is UnitHealth's index
	want := map[ChangeKind]Change{
		ChangeGOState:    {GUID: door, Kind: ChangeGOState, Old: GOStateReady, New: GOStateActive},
		ChangeGOFlags:    {GUID: door, Kind: ChangeGOFlags, Old: 32, New: 5},
		ChangeGODynFlags: {GUID: door, Kind: ChangeGODynFlags, Old: 0, New: 1},
	}
	if len(ch) != 3 {
		t.Fatalf("only the game object changes are reported: %+v", ch)
	}
	for _, c := range ch {
		if want[c.Kind] != c {
			t.Fatalf("change %+v", c)
		}
	}
	// A repeat of the same values reports nothing.
	if ch := s.Apply([]Block{{Type: BlockValues, GUID: door, Values: map[uint16]uint32{GOState: GOStateActive, GOFlags: 5}}}); len(ch) != 0 {
		t.Fatalf("%+v", ch)
	}
	// Values that arrive before the create block (a create we missed) still belong to a game object.
	_ = s.Apply([]Block{{Type: BlockValues, GUID: chest, Values: map[uint16]uint32{GOState: 1}}})
	ch = s.Apply([]Block{{Type: BlockValues, GUID: chest, Values: map[uint16]uint32{GOState: 0}}})
	if len(ch) != 1 || ch[0].Kind != ChangeGOState {
		t.Fatalf("%+v", ch)
	}
	if got := s.GameObjects(GOFilter{}, 0, 0, 0); len(got) != 2 || got[0].GUID != door || got[1].Distance != -1 {
		t.Fatalf("an object with no position sorts last: %+v", got)
	}
	// A create block starts the object afresh.
	if ch := s.Apply([]Block{goBlock(door, 1, 1, 1, map[uint16]uint32{FieldEntry: 124371, GOFlags: 32, GOState: GOStateReady})}); len(ch) != 1 || ch[0].Kind != ChangeCreated {
		t.Fatalf("%+v", ch)
	}
}

func TestGOUsableAndNames(t *testing.T) {
	for _, c := range []struct {
		typ   uint8
		flags uint32
		want  bool
	}{
		{GOTypeDoor, 32, true}, {GOTypeDoor, GOFlagNoInteract, false}, {GOTypeDoor, GOFlagInUse | 32, false},
		{GOTypeGeneric, 0, false}, {3, GOFlagLocked, true}, {10, GOFlagInteractCond, false},
	} {
		if got := GOUsable(c.typ, c.flags); got != c.want {
			t.Errorf("GOUsable(%d, %#x) = %v", c.typ, c.flags, got)
		}
	}
	for name, id := range map[string]uint8{"door": 0, "Chest": 3, "spell focus": 8, "spell_focus": 8, "goober": 10, "7": 7} {
		if got, ok := GOTypeByName(name); !ok || got != id {
			t.Errorf("GOTypeByName(%q) = %d %v", name, got, ok)
		}
	}
	if _, ok := GOTypeByName("lever"); ok {
		t.Error("lever is no type")
	}
	if _, ok := GOTypeByName("99"); ok {
		t.Error("99 is no type")
	}
	if GOTypeName(8) != "spell_focus" || GOTypeName(200) != "200" || GOStateName(2) != "active_alt" || GOStateName(9) != "9" {
		t.Error("names")
	}
}

// Uldaman, 2026-10-13 (m910q, build 17): the agent teleported to the entrance, listed what the
// server showed, walked to the Keystone door and used it. Every game object create block of the
// run parses into a game object, the door's use shows up as the flags and state change the
// session reports, and nothing in the list is mistaken for a unit.
func TestCapturedUldamanGameObjects(t *testing.T) {
	recs := testfixture.Load(t, "m7-gameobjects.twcap")
	st := NewStore()
	var changes []Change
	for _, r := range recs {
		if r.Dir != worldconn.ServerToClient || r.Redacted {
			continue
		}
		body := r.Body
		switch r.Op {
		case opcode.SMSGUpdateObject:
		case opcode.SMSGCompressedUpdateObject:
			var err error
			if body, err = Inflate(body); err != nil {
				t.Fatal(err)
			}
		case opcode.SMSGNewWorld:
			st.Clear()
			continue
		default:
			continue
		}
		blocks, left, err := parseUpdate(body)
		if err != nil || left != 0 {
			t.Fatalf("%v, %d bytes left", err, left)
		}
		kept := blocks[:0]
		for _, b := range blocks {
			if b.Type != BlockOutOfRange { // the logout at the end of the run empties the view
				kept = append(kept, b)
			}
		}
		changes = append(changes, st.Apply(kept)...)
	}
	all := st.GameObjects(GOFilter{Limit: 100000}, -226.8, 49.09, -46.03)
	if len(all) < 50 {
		t.Fatalf("only %d game objects after the run", len(all))
	}
	byType := map[string]int{}
	for _, g := range all {
		byType[g.TypeName]++
		if g.Entry == 0 || g.Distance < 0 {
			t.Fatalf("a placed object has an entry and a position: %+v", g)
		}
	}
	for _, typ := range []string{"door", "chest", "chair", "spell_focus", "trap"} {
		if byType[typ] == 0 {
			t.Fatalf("no %s in %v", typ, byType)
		}
	}
	// The Keystone door, as the server left it after the use: open, in use, interaction refused.
	k, ok := st.GameObject(door, 0, 0, 0)
	if !ok || k.Entry != 124371 || k.TypeName != "door" || k.State != "active" || k.Flags != GOFlagInUse|GOFlagInteractCond || k.Usable {
		t.Fatalf("Keystone door %+v %v", k, ok)
	}
	// The Ancient Vault is flagged in use from the start: not usable.
	if v, ok := st.GameObject(0xF11001E5D1003856, 0, 0, 0); !ok || v.Flags != 33 || v.Usable {
		t.Fatalf("Ancient Vault %+v %v", v, ok)
	}
	// A locked chest and a chest that is not.
	if l, ok := st.GameObject(locked, 0, 0, 0); !ok || !l.Locked || l.Flags != GOFlagLocked {
		t.Fatalf("locked chest %+v %v", l, ok)
	}
	if c, ok := st.GameObject(chest, 0, 0, 0); !ok || c.Locked || c.DynFlags != 1 {
		t.Fatalf("food crate %+v %v", c, ok)
	}
	doorChanges := map[ChangeKind]Change{}
	for _, c := range changes {
		if c.GUID == door && c.Kind != ChangeCreated {
			doorChanges[c.Kind] = c
		}
	}
	if c := doorChanges[ChangeGOState]; c.Old != GOStateReady || c.New != GOStateActive {
		t.Fatalf("door state %+v", doorChanges)
	}
	if c := doorChanges[ChangeGOFlags]; c.Old != 32 || c.New != 5 {
		t.Fatalf("door flags %+v", doorChanges)
	}
	for _, u := range st.Units(Filter{Limit: 100000}, 0, 0, 0) {
		if u.GUID.IsGameObject() {
			t.Fatalf("a game object in the unit list: %+v", u)
		}
	}
}
