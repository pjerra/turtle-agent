package worldspawn

import (
	"fmt"
	"strings"
	"testing"
)

// packText renders packs as "id:[members]" so an assertion reads like the model does.
func packText(ps []Pack) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		gs := make([]string, len(p.Members))
		for k, g := range p.Members {
			gs[k] = fmt.Sprint(g)
		}
		out[i] = fmt.Sprintf("%d:[%s]", p.ID, strings.Join(gs, " "))
	}
	return out
}

// TestPacksFromGroupsAndLinks pins the pack identity rules: creature_groups rows with AGGRO_TOGETHER merge (a formation-only
// group does not), a link chain of any direction merges, and nothing else does.
func TestPacksFromGroupsAndLinks(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": newHeader +
			spawnRow(1, 10, "Pack A", 0, 0, 0, 0) + "\n" +
			spawnRow(2, 10, "Pack A", 3, 0, 0, 0) + "\n" +
			spawnRow(10, 11, "Formation", 0, 10, 0, 0) + "\n" +
			spawnRow(11, 11, "Formation", 3, 10, 0, 0) + "\n" +
			spawnRow(20, 12, "Chained", 0, 20, 0, 0) + "\n" +
			spawnRow(21, 12, "Chained", 3, 20, 0, 0) + "\n" +
			spawnRow(22, 12, "Chained", 6, 20, 0, 0) + "\n" +
			join(30, 13, "Friendly", 60, 60, 1, 35, 0, 0, 30, 0, 0, 0, 0, 1, "", 20, 0, 1, 0, 0) + "\n" +
			join(31, 14, "Vendor", 60, 60, 0, 103, 1, 0, 31, 0, 0, 0, 0, 1, "", 20, 0, 1, 0, 0),
		"groups.tsv": "leader_guid\tmember_guid\tdist\tangle\tflags\n" +
			"1\t1\t0\t0\t3\n" +
			"1\t2\t3\t0\t3\n" +
			"10\t11\t3\t0\t1\n",
		"links.tsv": "guid\tmaster_guid\tflag\n" +
			"21\t20\t1\n" +
			"22\t21\t1\n",
	})
	w, _, err := Load(dir, 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := packText(w.Packs(PackOptions{AssistYd: 10}))
	want := []string{"1:[1 2]", "10:[10]", "11:[11]", "20:[20 21 22]"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("packs = %v, want %v", got, want)
	}
	a := w.Packs(PackOptions{AssistYd: 10})[0]
	if a.Centroid.X != 1.5 || a.Centroid.Y != 0 || a.Centroid.Z != 0 {
		t.Errorf("pack 1 centroid = %+v, want the mean of its members", a.Centroid)
	}
	if a.Radius != 1.5 {
		t.Errorf("pack 1 radius = %g, want 1.5", a.Radius)
	}
	for _, p := range w.Packs(PackOptions{}) {
		for _, g := range p.Members {
			if g == 30 || g == 31 {
				t.Errorf("pack %d holds the non-hostile spawn %d", p.ID, g)
			}
		}
	}
}

// TestLinkTemplatesResolveTheNearestMasterWithinRange pins the entry-linked rule: each slave's master is the nearest spawn of
// the master entry within search_range (0 = no limit, and then the core takes the first master it loaded, which the export can
// only approximate by the lowest guid).
func TestLinkTemplatesResolveTheNearestMasterWithinRange(t *testing.T) {
	spawns := newHeader +
		spawnRow(900, 400, "Master", 0, 0, 0, 0) + "\n" +
		spawnRow(901, 400, "Master", 200, 0, 0, 0) + "\n" +
		spawnRow(910, 401, "Slave", 5, 0, 0, 0) + "\n" +
		spawnRow(911, 401, "Slave", 195, 0, 0, 0) + "\n" +
		spawnRow(912, 401, "Slave", 7, 0, 0, 0) + "\n" +
		spawnRow(902, 402, "Guid Master", 0, 50, 0, 0)
	links := "guid\tmaster_guid\tflag\n" +
		"912\t902\t1\n"
	// The 50 yd row links 910 to 900 and 911 to 901; the guid row of 912 wins over any template row (the core prefers it).
	w, notes, err := Load(writeExport(t, map[string]string{
		"creatures.tsv":      spawns,
		"links.tsv":          links,
		"link_templates.tsv": "entry\tmap\tmaster_entry\tflag\tsearch_range\n401\t700\t400\t1\t50\n",
	}), 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := packText(w.Packs(PackOptions{}))
	want := []string{"900:[900 910]", "901:[901 911]", "902:[902 912]"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("packs = %v, want %v", got, want)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "link_templates.tsv") {
		t.Errorf("no note about the entry links:\n%s", strings.Join(notes, "\n"))
	}

	// search_range 0 means no limit: every slave links to the first master, as the core does.
	w, _, err = Load(writeExport(t, map[string]string{
		"creatures.tsv":      spawns,
		"links.tsv":          links,
		"link_templates.tsv": "entry\tmap\tmaster_entry\tflag\tsearch_range\n401\t700\t400\t1\t0\n",
	}), 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got = packText(w.Packs(PackOptions{}))
	want = []string{"900:[900 910 911]", "901:[901]", "902:[902 912]"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("search_range 0 packs = %v, want %v", got, want)
	}
}

// TestPacksPatrolAndPooled pins the two per-pack flags: a waypoint mover (or a formation follower of one) makes the pack a
// patrol, and a pooled member makes it possibly absent.
func TestPacksPatrolAndPooled(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": newHeader +
			spawnRowOpts(70, 300, "Patroller", 0, 0, 0, 2, 0, 5, 20, 0, 1, 0, 0) + "\n" +
			spawnRowOpts(71, 301, "Follower", 3, 0, 0, 0, 70, 0, 20, 0, 1, FormationMove, 0) + "\n" +
			spawnRowOpts(80, 302, "Pooled", 10, 0, 0, 0, 0, 0, 20, 0, 1, 0, 4),
		"groups.tsv": "leader_guid\tmember_guid\tdist\tangle\tflags\n" +
			"70\t71\t3\t0\t1\n",
		"movement.tsv": "guid\tpoint\tx\ty\tz\twaittime\n" +
			"70\t1\t0\t0\t0\t0\n" +
			"70\t2\t10\t0\t0\t0\n",
	})
	w, _, err := Load(dir, 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byID := map[uint64]Pack{}
	for _, p := range w.Packs(PackOptions{}) {
		byID[p.ID] = p
	}
	if !byID[70].Patrol {
		t.Errorf("pack 70: patrol false, its member walks waypoints")
	}
	if !byID[71].Patrol {
		t.Errorf("pack 71: patrol false, it is a formation follower of a patroller")
	}
	if !byID[80].Pooled {
		t.Errorf("pack 80: pooled false, its member has pool 4")
	}
	if byID[71].Pooled || byID[70].Pooled {
		t.Errorf("packs 70/71 marked pooled without a pool")
	}
}
