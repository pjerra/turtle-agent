package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

const routeOracleUsage = "usage: turtle-agent route-oracle --plans DIR --mmaps DIR --out DIR [--mode walk|bosses] [--objectives plan|bosses] [--plan NAME]..."

// The two modes. walk (the default) plans each plan's own stored sequence hop by hop and
// scores the stored hops against the navmesh; bosses plans entrance -> each leg's objective
// live, with no hand points and no break hops, to see what a boss list alone could do.
const (
	modeWalk   = "walk"
	modeBosses = "bosses"
)

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
	out := fs.String("out", "", "folder for <plan>.txt and <plan>.json (<plan>-bosses.* in bosses mode, <plan>-bosses-only.* with --objectives bosses); required")
	objectives := fs.String("objectives", routeoracle.ObjectivesPlan, "bosses mode only. plan: each leg's own objective (its boss, else its to point); bosses: ONLY the chain entrance -> boss 1 -> boss 2 ... (a leg without a boss is folded into the next boss; no stored to point is an objective)")
	mode := fs.String("mode", modeWalk, "walk: score the plan's stored hops on the navmesh; bosses: plan entrance -> each boss live, with no hand points")
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
	if *mode != modeWalk && *mode != modeBosses {
		fmt.Fprintf(stderr, "route-oracle: unknown --mode %q (want %s or %s)\n", *mode, modeWalk, modeBosses)
		return exitUsage
	}
	if *objectives != routeoracle.ObjectivesPlan && *objectives != routeoracle.ObjectivesBosses {
		fmt.Fprintf(stderr, "route-oracle: unknown --objectives %q (want %s or %s)\n", *objectives, routeoracle.ObjectivesPlan, routeoracle.ObjectivesBosses)
		return exitUsage
	}
	if *mode == modeWalk && *objectives != routeoracle.ObjectivesPlan {
		fmt.Fprintf(stderr, "route-oracle: --objectives %s needs --mode %s (the walk mode plans the plan's own stored sequence)\n", *objectives, modeBosses)
		return exitUsage
	}
	outDir, err := safeOutDir(*out)
	if err != nil {
		fmt.Fprintf(stderr, "route-oracle: %v\n", err)
		return exitUsage
	}
	*out = outDir
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
		// The plan file's own name must be a plan name and the one asked for: the report is
		// written under the name, so a name read from the JSON ("../escape") must never
		// reach a path — the file names below come from the validated CLI name, and Oracle
		// re-checks the plan's own.
		if err := routeoracle.CheckName(p.Name); err != nil {
			fmt.Fprintf(stderr, "route-oracle: %s.json: %v\n", n, err)
			failed = true
			continue
		}
		if p.Name != n {
			fmt.Fprintf(stderr, "route-oracle: %s.json says name %q, want %q\n", n, p.Name, n)
			failed = true
			continue
		}
		t0 := time.Now()
		var summary string
		var tiles []routeoracle.TileFile
		if *mode == modeBosses {
			rep, err := routeoracle.Bosses(planner, *mmaps, p, *objectives)
			if err == nil {
				err = writeBosses(rep, *out, n)
			}
			if err != nil {
				fmt.Fprintf(stderr, "route-oracle: %v\n", err)
				failed = true
				continue
			}
			summary, tiles = rep.Summary(), rep.Tiles
			for _, f := range failedBossLegs(rep) {
				summary += "\n  " + f
			}
		} else {
			rep, err := routeoracle.Oracle(planner, *mmaps, p)
			if err == nil {
				err = writeOracle(rep, *out, n)
			}
			if err != nil {
				fmt.Fprintf(stderr, "route-oracle: %v\n", err)
				failed = true
				continue
			}
			summary, tiles = rep.Summary(), rep.Tiles
		}
		fmt.Fprintf(stdout, "%s (%.0f ms wall)\n%s\n", p.Name, float64(time.Since(t0).Microseconds())/1000, summary)
		for _, t := range tiles {
			fmt.Fprintf(stdout, "  %s %s (%d bytes)\n", t.Name, t.Sha256, t.Bytes)
		}
	}
	if failed {
		return exitRouteOracle
	}
	return exitOK
}

// writeOracle writes the plan's <name>.txt and <name>.json into dir, under the validated
// CLI name (never the plan JSON's own), and refuses a destination that resolves outside dir.
func writeOracle(rep routeoracle.Report, dir, name string) error {
	txt, err := oracleText(rep)
	if err != nil {
		return err
	}
	return writeReport(rep, txt, dir, name)
}

// writeBosses writes the plan's <name>-bosses.txt and <name>-bosses.json, under the validated
// CLI name like writeOracle.
func writeBosses(rep routeoracle.BossesReport, dir, name string) error {
	txt, err := bossesText(rep)
	if err != nil {
		return err
	}
	stem := name + "-bosses"
	if rep.Objectives == routeoracle.ObjectivesBosses {
		stem = name + "-bosses-only"
	}
	return writeReport(rep, txt, dir, stem)
}

// writeReport writes <stem>.json (the report) and <stem>.txt (the table) into dir.
func writeReport(rep any, txt []byte, dir, name string) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return writeFilesIn(dir, map[string][]byte{name + ".json": append(b, '\n'), name + ".txt": txt})
}

// oracleText is the plan's human table: one line per leg, then the failed legs' hops, the
// touched breaks, the packs off the path, the stored points that are not redundant, the
// path's reasons, and the files used with their SHA-256.
func oracleText(rep routeoracle.Report) ([]byte, error) {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "%s (map %d, status %q, %d avoid boxes)\n", rep.Plan, rep.Map, rep.Status, rep.Avoid)
	fmt.Fprintln(w, rep.Summary())
	fmt.Fprintln(w, "metrics against the avoid-aware route (a scratch planner with SetAvoid of the plan's boxes); failed and indeterminate legs score nothing")
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
		if lr.Reason != "" {
			fmt.Fprintf(w, "leg %d (%s): %s: %s\n", lr.Leg, lr.Name, lr.Status, lr.Reason)
		}
		if lr.Status == "complete" {
			continue // a complete leg's hops say nothing the table does not
		}
		for j := range lr.Segments {
			seg := &lr.Segments[j]
			mark := ""
			if seg.NonComparable {
				mark = ", non-comparable (planned on from the point the partial path reached)"
			}
			fmt.Fprintf(w, "leg %d (%s): hop %d (%s) %.1f,%.1f -> %.1f,%.1f: plain %s, avoid %s, live %.1f yd, stored %.1f yd, ratio %.2f%s\n",
				lr.Leg, lr.Name, seg.Seq, seg.Kind, seg.From.X, seg.From.Y, seg.At.X, seg.At.Y,
				seg.Plain.Status, seg.Avoid.Status, seg.Avoid.LiveLenYd, seg.StoredLenYd, seg.Ratio, mark)
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
			if !s.Redundant && isApproachVia(s.Kind) {
				fmt.Fprintf(w, "leg %d (%s): stored %s (seq %d) at %.1f,%.1f,%.1f is %.1f yd from the route\n",
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
