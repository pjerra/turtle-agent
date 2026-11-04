package main

// pathavoid: the offline navmesh path with the plan's avoid boxes kept out of.
// usage: pathavoid -mmaps DIR -map 816 -box x0 y0 z0 x1 y1 z1[,x0 y0 z0 x1 y1 z1...] -from x y z -to x y z
import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

func main() {
	mm := flag.String("mmaps", "", "mmaps dir")
	m := flag.Uint("map", 0, "map id")
	boxes := flag.String("box", "", "avoid boxes: x0 y0 z0 x1 y1 z1[;...]")
	from := flag.String("from", "", "x y z")
	to := flag.String("to", "", "x y z")
	flag.Parse()
	pl := navmesh.NewPlanner(*mm)
	defer pl.Close()
	var bs []navmesh.Box
	for _, spec := range strings.Split(*boxes, ";") {
		if strings.TrimSpace(spec) == "" {
			continue
		}
		var v [6]float64
		for i, f := range strings.Fields(spec) {
			v[i], _ = strconv.ParseFloat(f, 64)
		}
		bs = append(bs, navmesh.Box{Min: navmesh.Vec3{float32(v[0]), float32(v[1]), float32(v[2])}, Max: navmesh.Vec3{float32(v[3]), float32(v[4]), float32(v[5])}})
	}
	pl.SetAvoid(uint32(*m), bs)
	var a, b [3]float32
	for i, f := range strings.Fields(*from) {
		x, _ := strconv.ParseFloat(f, 64)
		a[i] = float32(x)
	}
	for i, f := range strings.Fields(*to) {
		x, _ := strconv.ParseFloat(f, 64)
		b[i] = float32(x)
	}
	p, err := pl.Plan(uint32(*m), a, b)
	if err != nil {
		fmt.Println("ERR", err)
		return
	}
	fmt.Printf("HOP %v -> %v status %s length %.1f points %d\n", a, b, p.Status, p.Length, len(p.Points))
	for _, q := range p.Points {
		fmt.Printf("P %.2f %.2f %.2f\n", q[0], q[1], q[2])
	}
}
