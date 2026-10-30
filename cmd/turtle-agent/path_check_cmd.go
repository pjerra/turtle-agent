package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

const pathCheckUsage = "usage: turtle-agent path-check --mmaps DIR --map N [--json] -- x y z x y z"

const pathCheckCoords = 6

// exitCheckFailed is the exit code of path-check when a query itself fails (no
// tiles, no engine): usage errors stay exitUsage (2).
const exitCheckFailed = 1

// pathCheckCmd replays one hop with both queries: the offline Planner.Plan and the
// server's exact query (ServerPath plus ServerMoveVerdict). This is how a live refusal
// is replayed from the logged lead position. Exit 0 on a replayed hop, 1 when a query
// fails, 2 on a usage error.
func pathCheckCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("path-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	mapID := fs.String("map", "", "map id (numeric); required")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if len(fs.Args()) != pathCheckCoords || *mmaps == "" || *mapID == "" {
		fmt.Fprintln(stderr, pathCheckUsage)
		return exitUsage
	}
	var v [pathCheckCoords]float64
	for i, s := range fs.Args() {
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			fmt.Fprintln(stderr, pathCheckUsage)
			return exitUsage
		}
		v[i] = f
	}
	n, err := strconv.Atoi(*mapID)
	if err != nil || n < 0 {
		fmt.Fprintln(stderr, pathCheckUsage)
		return exitUsage
	}
	from := navmesh.Vec3{float32(v[0]), float32(v[1]), float32(v[2])}
	to := navmesh.Vec3{float32(v[3]), float32(v[4]), float32(v[5])}
	pl := navmesh.NewPlanner(*mmaps)
	defer pl.Close()
	path, err := pl.Plan(uint32(n), from, to)
	if err != nil {
		fmt.Fprintf(stderr, "path-check: %v\n", err)
		return exitCheckFailed
	}
	sp, err := pl.ServerPath(uint32(n), from, to)
	if err != nil {
		fmt.Fprintf(stderr, "path-check: %v\n", err)
		return exitCheckFailed
	}
	verdict := navmesh.ServerMoveVerdict(sp, from, to, 0)
	if *asJSON {
		fmt.Fprintf(stdout, "{\"offline\":{\"status\":%q,\"length\":%.1f,\"reason\":%q},\"server\":{\"type\":%d,\"verdict\":%q,\"actual_end\":[%.1f,%.1f,%.1f],\"short\":%.1f,\"start_dist\":%.1f,\"end_dist\":%.1f}}\n",
			path.Status, path.Length, path.Reason, sp.Type, verdict,
			sp.ActualEnd[0], sp.ActualEnd[1], sp.ActualEnd[2], dist3f(sp.ActualEnd, to), sp.StartDist, sp.EndDist)
		return exitOK
	}
	off := string(path.Status)
	if path.Reason != "" {
		off += " " + path.Reason
	}
	fmt.Fprintf(stdout, "offline: %s %.1f yd\n", off, path.Length)
	if sp.Type&navmesh.PathNoPath != 0 {
		fmt.Fprintf(stdout, "server: NOPATH %s start poly %.1f yd, end poly %.1f yd\n",
			verdict, sp.StartDist, sp.EndDist)
		return exitOK
	}
	short := dist3f(sp.ActualEnd, to)
	fmt.Fprintf(stdout, "server: type 0x%x %s end %.1f %.1f %.1f %.1f yd short, start poly %.1f yd, end poly %.1f yd\n",
		sp.Type, verdict, sp.ActualEnd[0], sp.ActualEnd[1], sp.ActualEnd[2], short, sp.StartDist, sp.EndDist)
	return exitOK
}

func dist3f(a, b navmesh.Vec3) float32 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
}
