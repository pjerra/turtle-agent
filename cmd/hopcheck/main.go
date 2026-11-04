// hopcheck replays the run leader's walk over a list of exported plan rows with
// the server's exact path query and verdicts. Local tool for the dmr-fix2 audit;
// never committed.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

type row struct {
	Leg  int     `json:"leg"`
	Seq  int     `json:"seq"`
	Kind string  `json:"kind"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Z    float64 `json:"z"`
}

func d3(a, b navmesh.Vec3) float64 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func main() {
	mmaps := flag.String("mmaps", "", "mmaps dir")
	m := flag.Uint("map", 0, "map id")
	in := flag.String("in", "", "JSON file: {rows:[...]} with the exported rows in walk order")
	floors := flag.Bool("floors", false, "print floors at the rows instead of replaying")
	zlo := flag.Float64("zlo", 0, "floors: lowest z")
	zhi := flag.Float64("zhi", 0, "floors: highest z")
	flag.Parse()
	var data struct {
		Rows []row `json:"rows"`
	}
	f, err := os.Open(*in)
	if err != nil {
		panic(err)
	}
	if err := json.NewDecoder(bufio.NewReader(f)).Decode(&data); err != nil {
		panic(err)
	}
	f.Close()
	pl := navmesh.NewPlanner(*mmaps)
	defer pl.Close()
	mapID := uint32(*m)
	if *floors {
		var at [][4]float32
		for _, r := range data.Rows {
			at = append(at, [4]float32{float32(r.X), float32(r.Y), float32(*zlo), float32(*zhi)})
		}
		printFloors(pl, mapID, at)
		return
	}
	refused, arrived, ok := 0, 0, 0
	prev := data.Rows[0]
	for i := 1; i < len(data.Rows); i++ {
		r := data.Rows[i]
		from := navmesh.Vec3{float32(prev.X), float32(prev.Y), float32(prev.Z)}
		to := navmesh.Vec3{float32(r.X), float32(r.Y), float32(r.Z)}
		cur := from
		verdict := ""
		if _, err := pl.Plan(mapID, from, to); err != nil {
			panic(err)
		}
		for dbg := 0; dbg < 2; dbg++ {
			sp, err := pl.ServerPath(mapID, from, to)
			if err != nil { panic(err) }
			fmt.Printf("DEBUG pass%d ty=0x%x len=%.1f start=%.2f end=%.2f from=%v to=%v\n", dbg, sp.Type, sp.Length, sp.StartDist, sp.EndDist, []float32{from[0],from[1],from[2]}, []float32{to[0],to[1],to[2]})
		}
		for try := 0; try < 3; try++ {
			if d3(cur, to) <= 3.0 {
				verdict = "arrived"
				break
			}
			sp, err := pl.ServerPath(mapID, cur, to)
			if err != nil {
				panic(err)
			}
			fmt.Printf("DEBUG ty=0x%x start=%.2f end=%.2f actual=%.2f %.2f %.2f cur=%.3f %.3f %.3f to=%.3f %.3f %.3f\n",
				sp.Type, sp.StartDist, sp.EndDist, sp.ActualEnd[0], sp.ActualEnd[1], sp.ActualEnd[2],
				cur[0], cur[1], cur[2], to[0], to[1], to[2])
			v := navmesh.ServerMoveVerdict(sp, cur, to, 0)
			if v != "ok" {
				verdict = fmt.Sprintf("REFUSED %s type 0x%x", v, sp.Type)
				break
			}
			if sp.Type&navmesh.PathNormal != 0 {
				verdict = "ok"
				break
			}
			cur = sp.ActualEnd
			if try == 2 {
				verdict = fmt.Sprintf("REFUSED incomplete x3 ends %.1f %.1f %.1f", cur[0], cur[1], cur[2])
			}
		}
		switch {
		case verdict == "arrived":
			arrived++
		case verdict == "ok":
			ok++
		default:
			refused++
		}
		fmt.Printf("%d\t%d->%d\t%s->%s\tlen %6.1f\t%s\n", r.Leg, prev.Seq, r.Seq, prev.Kind, r.Kind, d3(from, to), verdict)
		prev = r
	}
	fmt.Printf("SUMMARY rows=%d ok=%d arrived_in_3yd=%d refused=%d\n", len(data.Rows), ok, arrived, refused)
}
