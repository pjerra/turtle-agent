package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/routeoracle"
)

// exitRouteOracle: route-oracle could not report every plan (a failing plan is not a usage error).
const exitRouteOracle = 12

const routeOracleUsage = "usage: turtle-agent route-oracle --plans DIR --mmaps DIR --out DIR [--plan NAME]..."

// routeOracleDefaultPlans are the plans the oracle reports when no --plan is given.
var routeOracleDefaultPlans = []string{"wailing_caverns", "ragefire_chasm", "uldaman", "zulfarrak", "deadmines"}

// planList is a repeatable --plan flag.
type planList struct{ names *[]string }

func (l *planList) String() string {
	if l.names == nil {
		return ""
	}
	return strings.Join(*l.names, ",")
}
func (l *planList) Set(v string) error {
	*l.names = append(*l.names, v)
	return nil
}

// routeOracleCmd compares each plan's stored route with what the navmesh plans for its
// objective chain (entrance, then per leg its boss, else its to/last step point), and writes
// <out>/<plan>.txt (a human table) and <out>/<plan>.json (the machine report, Stage 1's
// fixture). Every leg is planned twice: plain, and on a scratch planner with the plan's avoid
// boxes; the report's metrics are against the avoid-aware path, the one a run plans with.
func routeOracleCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("route-oracle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	plansDir := fs.String("plans", "dungeons", "the folder with the plan files")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	out := fs.String("out", "", "folder for <plan>.txt and <plan>.json; required")
	var plans []string
	fs.Var(&planList{&plans}, "plan", "a plan name; repeatable (default: "+strings.Join(routeOracleDefaultPlans, ", ")+")")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if len(plans) == 0 {
		plans = routeOracleDefaultPlans
	}
	if *mmaps == "" || *out == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, routeOracleUsage)
		return exitUsage
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(stderr, "route-oracle: %v\n", err)
		return exitUsage
	}
	planner := navmesh.NewPlanner(*mmaps)
	defer planner.Close()

	failed := false
	for _, n := range plans {
		if !planName.MatchString(n) {
			fmt.Fprintf(stderr, "route-oracle: %q is not a plan name (a-z, 0-9, _ and -)\n", n)
			return exitUsage
		}
		p, err := dungeon.Load(filepath.Join(*plansDir, n+".json"))
		if err != nil {
			fmt.Fprintf(stderr, "route-oracle: %v\n", err)
			failed = true
			continue
		}
		t0 := time.Now()
		rep, err := routeoracle.Oracle(planner, *mmaps, p)
		if err != nil {
			fmt.Fprintf(stderr, "route-oracle: %v\n", err)
			failed = true
			continue
		}
		if err := writeOracle(rep, *out); err != nil {
			fmt.Fprintf(stderr, "route-oracle: %v\n", err)
			failed = true
			continue
		}
		fmt.Fprintf(stdout, "%s (%.0f ms wall)\n%s\n", rep.Plan, float64(time.Since(t0).Microseconds())/1000, rep.Summary())
		for _, t := range rep.Tiles {
			fmt.Fprintf(stdout, "  %s %s (%d bytes)\n", t.Name, t.Sha256, t.Bytes)
		}
	}
	if failed {
		return exitRouteOracle
	}
	return exitOK
}

// writeOracle writes the plan's <plan>.txt and <plan>.json into dir.
func writeOracle(rep routeoracle.Report, dir string) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, rep.Plan+".json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	txt, err := oracleText(rep)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rep.Plan+".txt"), txt, 0o644)
}

// oracleText is the plan's human table: one line per leg, then the touched breaks, the packs
// off the path, the stored points that are not redundant, the path's reasons, and the files
// used with their SHA-256.
func oracleText(rep routeoracle.Report) ([]byte, error) {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "%s (map %d, status %q, %d avoid boxes)\n", rep.Plan, rep.Map, rep.Status, rep.Avoid)
	fmt.Fprintln(w, rep.Summary())
	fmt.Fprintln(w, "metrics against the avoid-aware path (a scratch planner with SetAvoid of the plan's boxes)")
	fmt.Fprintf(w, "plan time %.0f ms\n", rep.PlanMs)
	fmt.Fprintln(w, "leg\tname\tobjective\tfrom\tto\tplain\tavoid\tgap\tlive\tstored\tratio\tmaxd\tmeand\tzj\tbreaks\tpacks\tredundant")
	for i := range rep.Legs {
		lr := &rep.Legs[i]
		fmt.Fprintf(w, "%d\t%s\t%s\t%.0f,%.0f\t%.0f,%.0f\t%s\t%s\t%.1f\t%.1f\t%.1f\t%.2f\t%.1f\t%.1f\t%d\t%d/%d\t%d/%d\t%d/%d\n",
			lr.Leg, lr.Name, lr.Objective, lr.From.X, lr.From.Y, lr.To.X, lr.To.Y,
			lr.Plain.Status, lr.Avoid.Status, lr.Avoid.EndGapYd, lr.Avoid.LiveLenYd,
			lr.StoredLenYd, lr.Ratio, lr.StoredMaxYd, lr.StoredMeanYd, len(lr.ZJumps),
			lr.BreaksTouched, len(lr.Breaks), lr.PacksOnPath, len(lr.Pulls),
			redundantOf(lr), approachViaOf(lr))
	}
	for i := range rep.Legs {
		lr := &rep.Legs[i]
		if lr.Avoid.Reason != "" {
			fmt.Fprintf(w, "leg %d (%s): avoid %s: %s\n", lr.Leg, lr.Name, lr.Avoid.Status, lr.Avoid.Reason)
		}
		if lr.Plain.Status != lr.Avoid.Status && lr.Plain.Reason != "" {
			fmt.Fprintf(w, "leg %d (%s): plain %s: %s\n", lr.Leg, lr.Name, lr.Plain.Status, lr.Plain.Reason)
		}
		for _, b := range lr.Breaks {
			if b.Touched {
				fmt.Fprintf(w, "leg %d (%s): break via %d (%s) at %.1f,%.1f,%.1f: %s\n",
					lr.Leg, lr.Name, b.Via, b.Kind, b.At.X, b.At.Y, b.At.Z, b.Why)
			}
		}
		for _, g := range lr.Pulls {
			if !g.OnPath {
				fmt.Fprintf(w, "leg %d (%s): pull group %d (entries %v) nearest position %.1f yd (dz %.1f) off the path (radius %.0f yd)\n",
					lr.Leg, lr.Name, g.Group, g.Entries, g.NearYd, g.DZYd, g.RadiusYd)
			}
		}
		for _, s := range lr.Stored {
			if !s.Redundant && (s.Kind == "approach" || s.Kind == "via" || s.Kind == "boss_approach") {
				fmt.Fprintf(w, "leg %d (%s): stored %s (seq %d) at %.1f,%.1f,%.1f is %.1f yd from the path\n",
					lr.Leg, lr.Name, s.Kind, s.Seq, s.At.X, s.At.Y, s.At.Z, s.NearYd)
			}
		}
		for _, j := range lr.ZJumps {
			fmt.Fprintf(w, "leg %d (%s): z jump %.1f yd at %.1f,%.1f,%.1f\n", lr.Leg, lr.Name, j.DropYd, j.At[0], j.At[1], j.At[2])
		}
	}
	fmt.Fprintln(w, "tiles used (sha-256):")
	for _, t := range rep.Tiles {
		fmt.Fprintf(w, "  %s\t%s\t%d bytes\n", t.Name, t.Sha256, t.Bytes)
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// redundantOf is how many of the leg's stored approach and via points are redundant.
func redundantOf(lr *routeoracle.LegReport) int {
	n := 0
	for _, s := range lr.Stored {
		if s.Redundant && isApproachVia(s.Kind) {
			n++
		}
	}
	return n
}

// approachViaOf is how many approach and via points the leg stores.
func approachViaOf(lr *routeoracle.LegReport) int {
	n := 0
	for _, s := range lr.Stored {
		if isApproachVia(s.Kind) {
			n++
		}
	}
	return n
}

// isApproachVia says whether the stored point is an approach or via point (the redundancy
// check's kind; a boss's approach is one too).
func isApproachVia(kind string) bool {
	return kind == "approach" || kind == "via" || kind == "boss_approach"
}
