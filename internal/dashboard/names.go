package dashboard

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// The bots' feed names a creature by its entry id only, and a plan has no name field for one. The plan's notes do name them in
// passing ("The Stonevault Cave Lurkers (4850) spawn stealthed"), so the dashboard reads those: a run of capitalised words
// (with the usual small words between them) right before a number in brackets.
var noteName = regexp.MustCompile(`((?:[A-Z][A-Za-z'\-]*)(?: (?:of|the|and|de|[A-Z][A-Za-z'\-]*)){0,4}) \((\d{3,6})\)`)

// EntryNames maps the creature entries of plan (pull groups, bosses, their first kills, skipped entries) to a name found in
// the notes of the plan or of a leg. The first mention wins; entries without a mention are left out and the page says
// "creature <id>". Keys are decimal strings so that the map is a JSON object.
func EntryNames(plan *dungeon.Plan) map[string]string {
	used := map[uint32]bool{}
	for _, l := range plan.Legs {
		for _, g := range l.PullGroups {
			for _, e := range g.Entries {
				used[e] = true
			}
		}
		for _, e := range l.Skip {
			used[e] = true
		}
		if b := l.Boss; b != nil {
			used[b.Entry] = true
			for _, e := range b.First {
				used[e] = true
			}
		}
	}
	out := map[string]string{}
	scan := func(notes []string) {
		for _, n := range notes {
			for _, m := range noteName.FindAllStringSubmatch(n, -1) {
				id, err := strconv.ParseUint(m[2], 10, 32)
				if err != nil || !used[uint32(id)] {
					continue
				}
				name := strings.TrimSpace(m[1])
				for _, lead := range []string{"The ", "the "} {
					name = strings.TrimPrefix(name, lead)
				}
				if _, seen := out[m[2]]; !seen && name != "" {
					out[m[2]] = name
				}
			}
		}
	}
	for _, l := range plan.Legs {
		scan(l.Notes)
	}
	scan(plan.Notes)
	return out
}
