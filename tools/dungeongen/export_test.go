package main

// export_test.go pins the spawn export trash-check reads (spec C0): the 11 raid exports under testdata carry the spawn-model
// columns appended at the end of creatures.tsv and the linking, group and template-movement files, and export.sh declares the
// headers that data has. A script edit without a re-export (or a re-export with an old script) fails here, not in C1.

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// raidExportMaps are the maps the C0 re-export covers: the spec's 11 raids. upper_blackrock_spire is a raid plan but not one of them.
var raidExportMaps = []uint32{249, 309, 409, 469, 509, 531, 532, 533, 807, 814, 819}

var creaturesHead = strings.Fields("guid entry name level_min level_max rank faction npc_flags x y z wander movement_type group_leader patrol auras")

// creaturesAdded are the columns C0 appends, in this order, so the name-keyed reader sees the same columns as before.
var creaturesAdded = strings.Fields("detection_range flags_extra speed_walk group_flags pool")

// exportFiles are the files C0 adds (and movement.tsv, which existed): header -> whether its columns are numbers.
var exportFiles = []struct {
	name string
	head []string
}{
	{"links.tsv", strings.Fields("guid master_guid flag")},
	{"link_templates.tsv", strings.Fields("entry map master_entry flag search_range")},
	{"groups.tsv", strings.Fields("leader_guid member_guid dist angle flags")},
	{"movement.tsv", strings.Fields("guid point x y z waittime")},
	{"movement_template.tsv", strings.Fields("entry point x y z waittime")},
}

func tsvRows(t *testing.T, path string) ([]string, [][]string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for i, row := range rows {
		if len(row) != len(head) {
			t.Fatalf("%s:%d: %d fields, the header has %d", path, i+2, len(row), len(head))
		}
	}
	return head, rows
}

func isNum(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// TestRaidExportsCarryTheSpawnModel checks every raid export: the extended creatures.tsv header and numeric appended columns, the
// five auxiliary files with their exact headers, and that the auxiliary rows name spawns of the same map.
func TestRaidExportsCarryTheSpawnModel(t *testing.T) {
	wantHead := append(slices.Clone(creaturesHead), creaturesAdded...)
	for _, m := range raidExportMaps {
		dir := filepath.Join("testdata", "map"+strconv.Itoa(int(m)))
		head, rows := tsvRows(t, filepath.Join(dir, "creatures.tsv"))
		if !slices.Equal(head, wantHead) {
			t.Errorf("%s/creatures.tsv: header %v, want %v", dir, head, wantHead)
			continue
		}
		guids := map[string]string{} // guid -> movement_type
		entries := map[string]bool{} // entries with a movement_type 2 spawn
		for i, row := range rows {
			for j, name := range creaturesAdded {
				if v := row[len(creaturesHead)+j]; !isNum(v) || strings.HasPrefix(v, "-") {
					t.Errorf("%s/creatures.tsv:%d: %s %q", dir, i+2, name, v)
				}
			}
			guids[row[0]] = row[12] // movement_type
			if row[12] == "2" {
				entries[row[1]] = true
			}
			// group_flags and group_leader come from the same creature_groups rows: a flag with no leader column is one of them drifting.
			if row[len(creaturesHead)+3] != "0" && row[13] == "0" {
				t.Errorf("%s/creatures.tsv:%d: group_flags %s but group_leader 0", dir, i+2, row[len(creaturesHead)+3])
			}
		}
		for _, f := range exportFiles {
			path := filepath.Join(dir, f.name)
			head, rows := tsvRows(t, path)
			if !slices.Equal(head, f.head) {
				t.Errorf("%s: header %v, want %v", path, head, f.head)
				continue
			}
			// Every column of the auxiliary files is a number (no names): a quoted or escaped value is a broken export.
			for i, row := range rows {
				for j, v := range row {
					if !isNum(v) {
						t.Errorf("%s:%d: %q in column %s is not a number", path, i+2, v, f.head[j])
					}
				}
			}
			switch f.name {
			case "links.tsv":
				for i, row := range rows {
					if _, ok := guids[row[0]]; !ok {
						t.Errorf("%s:%d: guid %s is not a spawn of the map", path, i+2, row[0])
					}
				}
			case "groups.tsv":
				for i, row := range rows {
					if _, ok := guids[row[1]]; !ok {
						t.Errorf("%s:%d: member %s is not a spawn of the map", path, i+2, row[1])
					}
				}
			case "movement.tsv":
				for i, row := range rows {
					if _, ok := guids[row[0]]; !ok {
						t.Errorf("%s:%d: %s is not a spawn of the map", path, i+2, row[0])
					}
				}
			case "movement_template.tsv":
				for i, row := range rows {
					if !entries[row[0]] {
						t.Errorf("%s:%d: entry %s has no movement_type 2 spawn on the map", path, i+2, row[0])
					}
				}
			}
		}
	}
}

// printfHeader returns the header export.sh declares for one output file: the printf in the block that redirects into it.
func printfHeader(t *testing.T, script, file string) []string {
	t.Helper()
	lines := strings.Split(script, "\n")
	at := -1
	for i, l := range lines {
		if strings.Contains(l, `> "$out/`+file+`"`) {
			if at >= 0 {
				t.Fatalf("export.sh writes %s in two places", file)
			}
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("export.sh does not write %s", file)
	}
	start := at - 1
	for ; start >= 0; start-- {
		if strings.TrimSpace(lines[start]) == "{" {
			break
		}
	}
	for i := start + 1; i < at; i++ {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "printf '") {
			continue
		}
		s := strings.TrimPrefix(l, "printf '")
		j := strings.Index(s, "'")
		if j < 0 {
			break
		}
		h := strings.NewReplacer(`\t`, "\t", `\n`, "").Replace(s[:j])
		return strings.Split(h, "\t")
	}
	t.Fatalf("export.sh: no printf header in the block that writes %s", file)
	return nil
}

// TestExportScriptDeclaresTheDataItWrote: the script's headers are the ones the raid exports have, so neither moved alone.
func TestExportScriptDeclaresTheDataItWrote(t *testing.T) {
	b, err := os.ReadFile("export.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	want := map[string][]string{"creatures.tsv": append(slices.Clone(creaturesHead), creaturesAdded...)}
	for _, f := range exportFiles {
		want[f.name] = f.head
	}
	for _, name := range []string{"creatures.tsv", "links.tsv", "link_templates.tsv", "groups.tsv", "movement.tsv", "movement_template.tsv"} {
		if got := printfHeader(t, script, name); !slices.Equal(got, want[name]) {
			t.Errorf("export.sh declares %s as %v, want %v", name, got, want[name])
		}
	}
}
