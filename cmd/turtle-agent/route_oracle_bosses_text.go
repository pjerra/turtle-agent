package main

import (
	"bytes"
	"fmt"
	"text/tabwriter"

	"github.com/pjerra/turtle-agent/internal/routeoracle"
)

// failedLine says why a leg's live plan did not reach its objective and which stored break it
// ran into.
func failedLine(l *routeoracle.BossLeg) string {
	s := fmt.Sprintf("leg %d (%s): %s, the live path ends %.1f yd short of the %s at %.1f,%.1f,%.1f (from %.1f,%.1f,%.1f)",
		l.Leg, l.Name, l.Status, l.Live.EndGapYd, l.Objective, l.To.X, l.To.Y, l.To.Z, l.From.X, l.From.Y, l.From.Z)
	if l.Status == "partial" {
		s += fmt.Sprintf("; it stops at %.1f,%.1f,%.1f", l.Live.End.X, l.Live.End.Y, l.Live.End.Z)
	}
	if len(l.BreakHits) == 0 {
		return s + "; no stored break bridges it or lies within 15 yd of the end or of the line to the objective"
	}
	for _, b := range l.BreakHits {
		s += fmt.Sprintf("; stored break leg %d via %d (%s) at %.1f,%.1f,%.1f (%s)", b.Leg, b.Via, b.Kind, b.At.X, b.At.Y, b.At.Z, b.Why)
	}
	return s
}

// failedBossLegs is the report's failed legs, one line each: the fair failures only (a
// non-comparable continuation is not a finding about the plan).
func failedBossLegs(rep routeoracle.BossesReport) []string {
	var out []string
	for i := range rep.Legs {
		if rep.Legs[i].Failed() {
			out = append(out, failedLine(&rep.Legs[i]))
		}
	}
	return out
}

// bossesText is the plan's human table: the summary, one line per leg, the failed legs with the
// breaks they hit, the non-comparable continuations, then what the complete legs say about the
// stored hand data (packs off the live path, hand points farther than 10 yd, floor jumps).
func bossesText(rep routeoracle.BossesReport) ([]byte, error) {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "%s (map %d, status %q, %d avoid boxes), bosses mode, objectives: %s\n", rep.Plan, rep.Map, rep.Status, rep.Avoid, rep.Objectives)
	if rep.Objectives == routeoracle.ObjectivesBosses {
		fmt.Fprintf(w, "boss-only chain: entrance -> boss 1 -> boss 2 ...; a leg without a boss is folded into the next boss (name a+b), no stored to point is an objective; legs after the last boss are not planned: %v\n", rep.Unplanned)
	}
	fmt.Fprintln(w, rep.Summary())
	fmt.Fprintln(w, "each leg is one avoid-aware navmesh plan from the previous objective to this leg's objective: no hand points, no break hops;")
	fmt.Fprintln(w, "a leg that does not reach it FAILED and scores nothing; a leg planned from a failed leg's end is non-comparable (NC) and scores nothing")
	fmt.Fprintf(w, "plan time %.0f ms\n", rep.PlanMs)
	fmt.Fprintln(w, "packs: a pack position within its radius plus 18 yd of the live path (8 yd of height), the same definition as the walk mode; hand: via/to/approach/stand points within 10 yd of it (redundant); NC: non-comparable; bridged: the NC leg planned from the previous objective")
	fmt.Fprintln(w, "leg\tname\tobjective\tfrom\tto\tstatus\tgap\tlive\tpacks\thand\tzj\tbreaks hit\tNC\tbridged")
	for i := range rep.Legs {
		l := &rep.Legs[i]
		nc := ""
		if l.NonComparable {
			nc = "NC"
		}
		if l.Status == "complete" {
			fmt.Fprintf(w, "%d\t%s\t%s\t%.0f,%.0f\t%.0f,%.0f\t%s\t%.1f\t%.1f\t%d/%d\t%d/%d\t%d\t-\t%s\t%s\n",
				l.Leg, l.Name, l.Objective, l.From.X, l.From.Y, l.To.X, l.To.Y, l.Status, l.Live.EndGapYd, l.Live.LiveLenYd,
				l.PacksOnPath, len(l.Packs), l.HandRedundant, len(l.Hand), len(l.ZJumps), nc, bridgedOf(l))
			continue
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%.0f,%.0f\t%.0f,%.0f\t%s\t%.1f\t%.1f\t-\t-\t-\t%d\t%s\t%s\n",
			l.Leg, l.Name, l.Objective, l.From.X, l.From.Y, l.To.X, l.To.Y, l.Status, l.Live.EndGapYd, l.Live.LiveLenYd, len(l.BreakHits), nc, bridgedOf(l))
	}
	for i := range rep.Legs {
		l := &rep.Legs[i]
		switch {
		case l.Failed():
			fmt.Fprintf(w, "FAILED %s\n", failedLine(l))
			if l.Reason != "" {
				fmt.Fprintf(w, "  planner: %s\n", l.Reason)
			}
		case l.NonComparable:
			fmt.Fprintf(w, "NON-COMPARABLE leg %d (%s): %s, %s: %s\n", l.Leg, l.Name, l.Status, l.NonComparableWhy, l.Reason)
			if b := l.Bridged; b != nil && b.Status != "complete" {
				fmt.Fprintf(w, "  BRIDGED leg %d is a second failure: %s\n", l.Leg, failedLine(b))
			}
		}
	}
	for i := range rep.Legs {
		l := &rep.Legs[i]
		if !l.Counted() {
			continue
		}
		for _, g := range l.Packs {
			if !g.OnPath {
				fmt.Fprintf(w, "leg %d (%s): pack group %d of stored leg %d (entries %v) nearest position %.1f yd (dz %.1f) from the live path, radius %.0f yd\n",
					l.Leg, l.Name, g.Group, legOf(g.Leg), g.Entries, g.NearYd, g.DZYd, g.RadiusYd)
			}
		}
		for _, h := range l.Hand {
			if !h.Redundant {
				fmt.Fprintf(w, "leg %d (%s): hand %s (stored leg %d, seq %d) at %.1f,%.1f,%.1f is %.1f yd from the live path\n",
					l.Leg, l.Name, h.Kind, legOf(h.Leg), h.Seq, h.At.X, h.At.Y, h.At.Z, h.NearYd)
			}
		}
		for _, b := range l.Breaks {
			fmt.Fprintf(w, "leg %d (%s): stored break (stored leg %d) via %d (%s) at %.1f,%.1f,%.1f: live path passes it %v\n",
				l.Leg, l.Name, legOf(b.Leg), b.Via, b.Kind, b.At.X, b.At.Y, b.At.Z, b.Touched)
		}
		for _, j := range l.ZJumps {
			fmt.Fprintf(w, "leg %d (%s): z jump %.1f yd at %.1f,%.1f,%.1f\n", l.Leg, l.Name, j.DropYd, j.At[0], j.At[1], j.At[2])
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

// bridgedOf is the table cell of a non-comparable leg's bridged re-plan.
func bridgedOf(l *routeoracle.BossLeg) string {
	if l.Bridged == nil {
		return "-"
	}
	if l.Bridged.Status == "complete" {
		return fmt.Sprintf("complete %.0f yd, packs %d/%d, hand %d/%d", l.Bridged.Live.LiveLenYd, l.Bridged.PacksOnPath, len(l.Bridged.Packs),
			l.Bridged.HandRedundant, len(l.Bridged.Hand))
	}
	return fmt.Sprintf("%s, %.0f yd short", l.Bridged.Status, l.Bridged.Live.EndGapYd)
}

// legOf is a folded row's stored leg index (0 when the field is absent).
func legOf(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
