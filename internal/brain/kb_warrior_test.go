package brain

import (
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// The KB warrior rotations play: one fake warrior of level 60 per spec, full rage, one mob in reach (kb_rotations_test.go's
// rigs), pinned on the file's first entry that can run and on a core spell of the recipe appearing right after it.

// Spells of the KB warrior tests not already named in warrior_test.go (ids from internal/spells/meta.tsv.gz).
const (
	spKBSlam       uint32 = 1464  // Slam rank 1
	spKBMaster     uint32 = 54023 // Master Strike (Turtle)
	spKBDeadWish   uint32 = 12328 // Death Wish (talent)
	spKBReckless   uint32 = 1719  // Recklessness
	spKBChallenger uint32 = 1161  // Challenging Shout
)

// kbFullWarrior is a level 60 warrior of the preset with every spell of its KB file known, on 100 rage, opening the standard fight.
func kbFullWarrior(t *testing.T, preset, stance, rotation string, known ...uint32) (*rig, *meleeFake) {
	t.Helper()
	r, mf := newWarriorRig(t, preset, stance, Params{Rotation: rotation}, known...)
	r.g.setSelf(func(u *objects.Unit) { u.Level = 60 })
	setRage(r.g, 1000)
	meleeFight(r, mf)
	return r, mf
}

// kbSunderFull gives the mob the five stacks the file's opener Sunder waits on: the fake does not apply the aura itself, and
// without the stacks the opener would cast again instead of the next entry.
func kbSunderFull(r *rig) {
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Sunder Armor", SpellID: spSunder, Harmful: true, Stacks: 5}}
	})
}

func TestKBArmsWarriorPlays(t *testing.T) {
	r, _ := kbFullWarrior(t, "arms_warrior", stanceBattle, "kb_arms_warrior",
		spBattleSt, spDefSt, spBerserkSt, spCharge, spSunder, spBloodrage, spExecute, spMortal, spKBSlam, spOverpower, spHamstring,
		spHeroic, spCleave, spSweeping, spBattleShout, spKBDeadWish, spKBReckless)
	actions(r, 100*time.Millisecond, 2200*time.Millisecond) // the opening delay, then the first cast
	kbSunderFull(r)
	actions(r, 1600*time.Millisecond)
	casts := r.g.castList()
	if len(casts) < 2 || casts[0].spell != spSunder {
		t.Fatalf("casts %v, want the opener's Sunder Armor first (idle %q)", casts, r.idle())
	}
	if casts[1].spell != spMortal {
		t.Fatalf("casts %v, want Mortal Strike (a core spell) within 3 s of the opener", casts)
	}
}

func TestKBFuryWarriorPlays(t *testing.T) {
	r, _ := kbFullWarrior(t, "fury_warrior", stanceBerserker, "kb_fury_warrior",
		spBattleSt, spBerserkSt, spCharge, spSunder, spBloodrage, spExecute, spBloodthirst, spWhirlwind, spKBSlam, spHeroic, spKBMaster,
		spHamstring, spCleave, spBattleShout, spBerserkRage, spKBDeadWish, spKBReckless)
	// Recklessness is band high and the KB lets it go "almost any time", so it is the file's first entry that can run,
	// then the opener's Sunder (one application), then the core rotation.
	actions(r, 100*time.Millisecond, 2200*time.Millisecond)
	kbSunderFull(r)
	actions(r, 1600*time.Millisecond, 1200*time.Millisecond)
	casts := r.g.castList()
	if len(casts) < 2 || casts[0].spell != spKBReckless || casts[1].spell != spSunder {
		t.Fatalf("casts %v, want Recklessness on the pull then the opener's Sunder Armor (idle %q)", casts, r.idle())
	}
	if len(casts) < 3 || casts[2].spell != spBloodthirst {
		t.Fatalf("casts %v, want Bloodthirst (a core spell) within 3 s of the opener", casts)
	}
}

func TestKBFuryTankWarriorPlays(t *testing.T) {
	r, _ := kbFullWarrior(t, "tank_warrior", stanceDefensive, "kb_fury_tank_warrior",
		spDefSt, spBattleSt, spBerserkSt, spBloodthirst, spRevenge, spHeroic, spSunder, spBloodrage, spCleave, spThunder, spKBChallenger)
	actions(r, 100*time.Millisecond, 2200*time.Millisecond)
	casts := r.g.castList()
	if len(casts) == 0 || casts[0].spell != spBloodthirst {
		t.Fatalf("casts %v, want Bloodthirst first: the stance and Bloodrage entries cannot run (idle %q)", casts, r.idle())
	}
}
