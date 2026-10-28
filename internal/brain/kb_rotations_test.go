package brain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// The rotation files generated from the Turtle knowledge base (tools/kbrotgen) are committed next to the recipes: the
// recipes are the human judgment, the generated files the playable list. These tests hold the two together (a hand edit
// of a generated file fails), check that every KB rotation resolves in the preset its recipe names, and play the pilot.

// kbModuleRoot is the module root, from this file's path (internal/brain/kb_rotations_test.go).
func kbModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: no file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// kbRecipe is the part of tools/kbrotgen/recipes/<name>.json these tests read.
type kbRecipe struct {
	Name     string   `json:"name"`
	Class    string   `json:"class"`
	Spec     string   `json:"spec"`
	Role     string   `json:"role"`
	KBFile   string   `json:"kb_file"`
	Preset   string   `json:"preset"`
	Replaces []string `json:"replaces"`
	Mode     string   `json:"mode"`
	Rows     []struct {
		From        string            `json:"from"`
		Index       int               `json:"index"`
		Text        string            `json:"text"`
		Unexpressed string            `json:"unexpressed"`
		Entries     []json.RawMessage `json:"entries"`
	} `json:"rows"`
	Order []string `json:"order"`
}

func TestKBRotationsMatchRecipes(t *testing.T) {
	root := kbModuleRoot(t)
	dir := filepath.Join(root, "tools", "kbrotgen", "recipes")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	recipes := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		recipes++
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var rec kbRecipe
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if rec.Name+".json" != e.Name() {
			t.Errorf("%s: the recipe names itself %q", e.Name(), rec.Name)
		}
		r, err := RotationNamed(rec.Name)
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		if r.Class != rec.Class || r.Spec != rec.Spec {
			t.Errorf("%s: the rotation is %s %s, the recipe %s %s", rec.Name, r.Class, r.Spec, rec.Class, rec.Spec)
		}
		if r.KB == nil {
			t.Errorf("%s: no kb block", rec.Name)
			continue
		}
		if r.KB.File != rec.KBFile || r.KB.Preset != rec.Preset {
			t.Errorf("%s: kb %q preset %q, the recipe says %q %q", rec.Name, r.KB.File, r.KB.Preset, rec.KBFile, rec.Preset)
		}
		if want := "tools/kbrotgen/recipes/" + e.Name(); r.KB.Recipe != want {
			t.Errorf("%s: kb.recipe %q, want %q", rec.Name, r.KB.Recipe, want)
		}
		if r.Mode != rec.Mode || !reflect.DeepEqual(r.Replaces, rec.Replaces) {
			t.Errorf("%s: mode %q replaces %v, the recipe says %q %v", rec.Name, r.Mode, r.Replaces, rec.Mode, rec.Replaces)
		}

		// Every recipe entry, once, in the order the recipe names.
		entry := map[string]json.RawMessage{}
		for _, row := range rec.Rows {
			for _, raw := range row.Entries {
				var id struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(raw, &id); err != nil {
					t.Fatalf("%s: entry: %v", rec.Name, err)
				}
				if _, ok := entry[id.ID]; ok {
					t.Errorf("%s: entry %q is in two recipe rows", rec.Name, id.ID)
				}
				entry[id.ID] = raw
			}
		}
		if len(r.Priorities) != len(rec.Order) {
			t.Errorf("%s: the rotation has %d entries, the order names %d", rec.Name, len(r.Priorities), len(rec.Order))
		}
		seen := map[string]bool{}
		for i, id := range rec.Order {
			if seen[id] {
				t.Errorf("%s: order names %q twice", rec.Name, id)
				continue
			}
			seen[id] = true
			raw, ok := entry[id]
			if !ok {
				t.Errorf("%s: order names %q, no recipe row has it", rec.Name, id)
				continue
			}
			var want Priority
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&want); err != nil {
				t.Fatalf("%s: %s: %v", rec.Name, id, err)
			}
			if i >= len(r.Priorities) {
				continue
			}
			wantJSON, _ := json.Marshal(want)
			gotJSON, _ := json.Marshal(r.Priorities[i])
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("%s: priority %d (%s) differs from the recipe:\n got %s\nwant %s", rec.Name, i+1, id, gotJSON, wantJSON)
			}
		}
		for id := range entry {
			if !seen[id] {
				t.Errorf("%s: entry %q is in no order", rec.Name, id)
			}
		}

		// The rows the rotation does not express, with the recipe's own text and reason.
		var wantU []KBRow
		for _, row := range rec.Rows {
			if row.Unexpressed == "" {
				continue
			}
			wantU = append(wantU, KBRow{Row: fmt.Sprintf("%s[%d]", row.From, row.Index), Text: row.Text, Why: row.Unexpressed})
		}
		if !reflect.DeepEqual(r.KB.Unexpressed, wantU) {
			t.Errorf("%s: kb.unexpressed:\n got %+v\nwant %+v", rec.Name, r.KB.Unexpressed, wantU)
		}
	}
	if recipes == 0 {
		t.Fatal("no recipes in tools/kbrotgen/recipes")
	}
}

func TestKBRotationsResolveInTheirPreset(t *testing.T) {
	found := 0
	for _, name := range RotationNames() {
		if !strings.HasPrefix(name, "kb_") {
			continue
		}
		found++
		r, err := RotationNamed(name)
		if err != nil {
			t.Fatal(err)
		}
		if r.KB == nil {
			t.Errorf("%s: no kb block, so no preset to play it in", name)
			continue
		}
		if err := CheckParams(Params{Strategies: []string{r.KB.Preset}, Rotation: name}); err != nil {
			t.Errorf("%s in %s: %v", name, r.KB.Preset, err)
			continue
		}
		// The engine keys actions by name: an entry id that is another active strategy's action would silently
		// replace it (or be replaced). The rotation itself is not one of those strategies.
		own := map[string]bool{}
		for _, p := range r.Priorities {
			own[p.ID] = true
		}
		strats, _, err := ResolveFor([]string{r.KB.Preset}, ResolveOpts{Rotation: name})
		if err != nil {
			t.Errorf("%s in %s: %v", name, r.KB.Preset, err)
			continue
		}
		for _, s := range strats {
			if s.Name == "rot:"+name {
				continue
			}
			for _, a := range s.Actions {
				if own[a.Name] {
					t.Errorf("%s: entry id %q is an action of %s", name, a.Name, s.Name)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no kb_ rotation")
	}
}

// The KB protection pilot's first two casts: Shield Slam, then Sunder Armor. The stance is the preset's job (the fake is
// in the stance warrior_tank keeps); 60 rage is enough for the rage dump, and the KB's filler comes before it.
const spKBShieldSlam uint32 = 23922

func TestKBProtectionWarriorPlays(t *testing.T) {
	r, mf := newWarriorRig(t, "tank_warrior", stanceDefensive, Params{Rotation: "kb_protection_warrior"},
		spKBShieldSlam, spSunder, spHeroic)
	r.g.setSelf(func(u *objects.Unit) { u.Level = 60 })
	setRage(r.g, 600)
	meleeFight(r, mf)
	actions(r, 100*time.Millisecond, 2200*time.Millisecond, 1600*time.Millisecond)
	var got []uint32
	for _, c := range r.g.castList() {
		got = append(got, c.spell)
	}
	if len(got) < 2 || got[0] != spKBShieldSlam || got[1] != spSunder {
		t.Fatalf("casts %v, want Shield Slam then Sunder Armor first (idle %q)", got, r.idle())
	}
}
