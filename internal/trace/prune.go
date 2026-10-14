package trace

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// generated matches the ids NewRunID makes; only their files are ever pruned.
var generated = regexp.MustCompile(`^r-\d{8}T\d{6}-[0-9a-f]{4}\.(jsonl|summary\.json)$`)

// Prune deletes the oldest generated-id traces (and their summaries) of dir beyond keep runs, never the run named current. The ids
// sort by start time. A folder the process cannot list is left alone.
func Prune(dir string, keep int, current string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	runs := map[string]bool{}
	for _, e := range ents {
		if n := e.Name(); !e.IsDir() && generated.MatchString(n) {
			runs[n[:strings.IndexByte(n, '.')]] = true
		}
	}
	ids := make([]string, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for len(ids) > keep {
		id := ids[0]
		ids = ids[1:]
		if id == current {
			continue
		}
		_ = os.Remove(filepath.Join(dir, id+".jsonl"))
		_ = os.Remove(filepath.Join(dir, id+".summary.json"))
	}
}
