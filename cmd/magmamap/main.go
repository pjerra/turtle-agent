package main

// magmamap: every polygon of a map whose flags hold the given bits, with its centre and z range.
import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

func main() {
	mm := flag.String("mmaps", "", "mmaps dir")
	m := flag.Uint("map", 0, "map id")
	bits := flag.Uint("bits", 2, "flag bits (2 magma, 4 slime, 8 water)")
	verts := flag.Bool("verts", false, "print every vertex")
	flag.Parse()
	_ = os.Args
	pl := navmesh.NewPlanner(*mm)
	defer pl.Close()
	polys, trunc, err := pl.Polygons(uint32(*m), -20000, -20000, 20000, 20000)
	if err != nil {
		panic(err)
	}
	fmt.Println("polys", len(polys), "truncated", trunc)
	n := 0
	for _, p := range polys {
		if p.Flags&uint16(*bits) == 0 {
			continue
		}
		var sx, sy, zmin, zmax float64
		zmin = 1e9
		zmax = -1e9
		for _, v := range p.Verts {
			sx += float64(v[0])
			sy += float64(v[1])
			if z := float64(v[2]); z < zmin {
				zmin = z
			} else if z > zmax {
				zmax = z
			}
		}
		k := float64(len(p.Verts))
		if *verts {
			fmt.Printf("POLY flags 0x%x z %.2f..%.2f", p.Flags, zmin, zmax)
			for _, v := range p.Verts {
				fmt.Printf(" %.2f %.2f %.2f", v[0], v[1], v[2])
			}
			fmt.Println()
		} else {
			fmt.Printf("%.1f %.1f z %.2f..%.2f flags 0x%x\n", sx/k, sy/k, zmin, zmax, p.Flags)
		}
		n++
	}
	fmt.Println("matched", n, strconv.Itoa(int(*bits)))
}
