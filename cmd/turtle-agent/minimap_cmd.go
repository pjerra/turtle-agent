package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/minimap"
)

const minimapUsage = "usage: turtle-agent minimap-extract --client <Data dir> --out <dir> [--maps 70,209|all-instances] [--mmaps DIR] [--list]\n" +
	"  reads the game client's MPQ archives and writes <out>/<map>.png + <map>.json for the dashboard (README \"Minimaps\")"

// minimapCmd extracts the in-game minimaps of maps from the client the owner owns.
func minimapCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("minimap-extract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "", "the client's Data folder (the MPQ archives; read only)")
	out := fs.String("out", "", "folder to write <map>.png and <map>.json into")
	maps := fs.String("maps", "all-instances", `map ids, comma separated, or "all-instances" (every dungeon and raid of Map.dbc)`)
	mmaps := fs.String("mmaps", "", "server mmaps folder(s), comma separated: a map with no minimap tiles gets a height-shaded floor render of its navmesh")
	prefer := fs.String("prefer", "", "order of the image sources for <map>.png, comma separated: minimap (the terrain tiles), worldmap (the map picture of the area), navmesh (default worldmap,minimap,navmesh)")
	list := fs.Bool("list", false, "only report what each map would get; write nothing")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || *client == "" || (*out == "" && !*list) {
		fmt.Fprintln(stderr, minimapUsage)
		return exitUsage
	}
	ids, all, err := minimap.ParseMapList(*maps)
	if err != nil {
		fmt.Fprintln(stderr, "minimap-extract:", err)
		return exitUsage
	}
	pref, err := minimap.ParsePrefer(*prefer)
	if err != nil {
		fmt.Fprintln(stderr, "minimap-extract:", err)
		return exitUsage
	}
	res, err := minimap.Run(minimap.Options{ClientDir: expandHome(*client), OutDir: expandHome(*out), MmapsDirs: splitDirs(*mmaps),
		Maps: ids, All: all, Prefer: pref, ListOnly: *list})
	if err != nil {
		fmt.Fprintln(stderr, "minimap-extract:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%-4s %-34s %-8s %-10s %-10s %s\n", "map", "name", "type", "image", "size", "other images")
	failed := 0
	for _, r := range res {
		size := "-"
		if r.Kind != minimap.KindNone {
			size = fmt.Sprintf("%dx%d", r.Meta.Width, r.Meta.Height)
		}
		note := r.Note
		if r.Error != nil {
			failed++
			note = "ERROR: " + r.Error.Error()
		}
		var others []string
		for _, l := range r.Layers {
			if l.Kind != r.Kind || l.Floor != "" {
				others = append(others, l.Kind+l.Floor)
			}
		}
		if len(others) > 0 {
			note = "also " + strings.Join(others, ",") + " " + note
		}
		fmt.Fprintf(stdout, "%-4d %-34s %-8s %-10s %-10s %s\n", r.ID, r.Name, typeName(r.Type), r.Kind, size, note)
	}
	if *out != "" && !*list {
		fmt.Fprintf(stdout, "wrote to %s\n", filepath.Clean(expandHome(*out)))
	}
	if failed > 0 {
		return 1
	}
	return exitOK
}

func typeName(t uint32) string {
	switch t {
	case minimap.InstanceParty:
		return "dungeon"
	case minimap.InstanceRaid:
		return "raid"
	case minimap.InstanceBG:
		return "bg"
	case minimap.InstanceArena:
		return "arena"
	}
	return "world"
}

// splitDirs reads a comma separated list of folders.
func splitDirs(s string) []string {
	var out []string
	for _, d := range strings.Split(s, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, expandHome(d))
		}
	}
	return out
}
