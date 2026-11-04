package main

// Scratch tool (not committed): queries the server's navmesh around a point.
// navq <mmaps> <map> floors x y z      -> surfaces at (x,y) from z-8 to z+8
// navq <mmaps> <map> near x y z        -> nearest walkable point + the polygons of a 4x4 yd box
// navq <mmaps> <map> plan x y z X Y Z  -> server path (status, points)
import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

func main() {
	pl := navmesh.NewPlanner(os.Args[1])
	defer pl.Close()
	m, _ := strconv.Atoi(os.Args[2])
	cmd := os.Args[3]
	f := func(i int) float32 { v, _ := strconv.ParseFloat(os.Args[i], 32); return float32(v) }
	x, y, z := f(4), f(5), f(6)
	switch cmd {
	case "floors":
		fl, err := pl.Floors(uint32(m), x, y, z-8, z+8)
		if err != nil {
			fmt.Println("ERR", err)
			return
		}
		for _, q := range fl {
			fmt.Printf("floor z %.2f flags 0x%02x\n", q.Z, q.Flags)
		}
		if len(fl) == 0 {
			fmt.Println("no floor")
		}
	case "near":
		p, ok, err := pl.Nearest(uint32(m), navmesh.Vec3{x, y, z})
		if err != nil {
			fmt.Println("ERR", err)
			return
		}
		fmt.Printf("nearest %.2f %.2f %.2f ok %v\n", p[0], p[1], p[2], ok)
		polys, trunc, err := pl.Polygons(uint32(m), x-3, y-3, x+3, y+3)
		if err != nil {
			fmt.Println("ERR", err)
			return
		}
		fmt.Printf("polys %d truncated %v\n", len(polys), trunc)
		sort.Slice(polys, func(i, j int) bool { return polys[i].Verts[0][2] < polys[j].Verts[0][2] })
		for i, q := range polys {
			var sx, sy float32
			lo, hi := q.Verts[0][2], q.Verts[0][2]
			for _, v := range q.Verts {
				sx += v[0]
				sy += v[1]
				if v[2] < lo {
					lo = v[2]
				}
				if v[2] > hi {
					hi = v[2]
				}
			}
			n := float32(len(q.Verts))
			fmt.Printf("  poly %2d flags 0x%02x n %d centre %.1f %.1f z %.2f..%.2f verts", i, q.Flags, len(q.Verts), sx/n, sy/n, lo, hi)
			for _, v := range q.Verts {
				fmt.Printf(" (%.1f %.1f %.1f)", v[0], v[1], v[2])
			}
			fmt.Println()
		}
	case "comp":
		found, n, bb, err := pl.Component(uint32(m), navmesh.Vec3{x, y, z})
		if err != nil {
			fmt.Println("ERR", err)
			return
		}
		fmt.Printf("comp found %v polys %d bbox %.1f %.1f %.2f .. %.1f %.1f %.2f\n", found, n, bb[0], bb[1], bb[2], bb[3], bb[4], bb[5])
	case "batch":
		// x0 y0 x1 y1 step | target x y z | anchor x y z | next x y z
		bx1, by1, bst, bz := f(6), f(7), f(8), f(9)
		tgt := navmesh.Vec3{f(10), f(11), f(12)}
		anc := navmesh.Vec3{f(13), f(14), f(15)}
		nxt := navmesh.Vec3{f(16), f(17), f(18)}
		for yy := by1; yy >= y; yy -= bst {
			for xx := x; xx <= bx1; xx += bst {
				d := float32(0)
				dx, dy := xx-tgt[0], yy-tgt[1]
				d = float32(math.Sqrt(float64(dx*dx + dy*dy)))
				p1, err := pl.Plan(uint32(m), anc, navmesh.Vec3{xx, yy, bz})
				if err != nil || p1.Status != navmesh.Complete {
					continue
				}
				p2, err := pl.Plan(uint32(m), navmesh.Vec3{xx, yy, bz}, nxt)
				ns := "fail"
				if err == nil && p2.Status == navmesh.Complete {
					ns = "ok"
				}
				fmt.Printf("CAND %.1f %.1f %.1f d %.1f anc %.1f nxt %s %.1f\n", xx, yy, bz, d, p1.Length, ns, p2.Length)
			}
		}
	case "comps":
		x1, y1, st := f(7), f(8), f(9)
		names := map[[6]float32]string{}
		alpha := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
		var order []string
		for yy := y1; yy >= y; yy -= st {
			line := ""
			for xx := x; xx <= x1; xx += st {
				found, n, bb, err := pl.Component(uint32(m), navmesh.Vec3{xx, yy, z})
				key := bb
				if !found || err != nil {
					line += "."
					continue
				}
				if names[key] == "" {
					lab := string(alpha[len(order)%len(alpha)])
					order = append(order, fmt.Sprintf("%s: %d polys bbox %.1f %.1f %.2f .. %.1f %.1f %.2f", lab, n, bb[0], bb[1], bb[2], bb[3], bb[4], bb[5]))
					names[key] = lab
				}
				line += names[key]
			}
			fmt.Printf("%8.0f %s\n", yy, line)
		}
		hdr := "         "
		for xx := x; xx <= x1; xx += st {
			if int(xx-x+0.01)%10 == 0 {
				hdr += "|"
			} else {
				hdr += "."
			}
		}
		fmt.Println(hdr)
		for _, o := range order {
			fmt.Println(o)
		}
		fmt.Printf("x %.0f..%.0f y %.0f..%.0f step %.0f at z %.1f\n", x, x1, y, y1, st, z)
	case "box":
		x1, y1 := f(6), f(7)
		polys, trunc, err := pl.Polygons(uint32(m), x, y, x1, y1)
		if err != nil {
			fmt.Println("ERR", err)
			return
		}
		fmt.Printf("polys %d truncated %v\n", len(polys), trunc)
		for i, q := range polys {
			var sx, sy float32
			lo, hi := q.Verts[0][2], q.Verts[0][2]
			for _, v := range q.Verts {
				sx += v[0]
				sy += v[1]
				if v[2] < lo {
					lo = v[2]
				}
				if v[2] > hi {
					hi = v[2]
				}
			}
			n := float32(len(q.Verts))
			fmt.Printf("poly %3d flags 0x%02x n %d centre %.1f %.1f z %.2f..%.2f verts", i, q.Flags, len(q.Verts), sx/n, sy/n, lo, hi)
			for _, v := range q.Verts {
				fmt.Printf(" (%.1f %.1f %.1f)", v[0], v[1], v[2])
			}
			fmt.Println()
		}
	case "grid":
		x1, y1, st := f(7), f(8), f(9)
		goal := navmesh.Vec3{f(10), f(11), f(12)}
		// x0 y0 z | x1 y1 step | gx gy gz
		for yy := y1; yy >= y; yy -= st {
			line := ""
			for xx := x; xx <= x1; xx += st {
				p, err := pl.Plan(uint32(m), navmesh.Vec3{xx, yy, z}, goal)
				c := "?"
				if err == nil {
					switch p.Status {
					case navmesh.Complete:
						c = "C"
					case navmesh.Partial:
						c = "P"
					case navmesh.NoPath:
						c = "N"
					}
				}
				line += c
			}
			fmt.Printf("%8.0f %s\n", yy, line)
		}
		// x header marks every 5 yd
		hdr := "         "
		for xx := x; xx <= x1; xx += st {
			if int(xx-x+0.01)%5 == 0 {
				hdr += "|"
			} else {
				hdr += "."
			}
		}
		fmt.Println(hdr)
		fmt.Printf("x %.0f..%.0f y %.0f..%.0f step %.0f, goal %.0f %.0f %.0f\n", x, x1, y, y1, st, goal[0], goal[1], goal[2])
	case "plan":
		to := navmesh.Vec3{f(7), f(8), f(9)}
		p, err := pl.Plan(uint32(m), navmesh.Vec3{x, y, z}, to)
		if err != nil {
			fmt.Println("ERR", err)
			return
		}
		fmt.Printf("status %s len %.1f reason %q\n", p.Status, p.Length, p.Reason)
		for _, q := range p.Points {
			fmt.Printf("P %.2f %.2f %.2f\n", q[0], q[1], q[2])
		}
	default:
		fmt.Println("unknown command")
	}
}
