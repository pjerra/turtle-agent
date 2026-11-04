package main

// liquiddump: JSON of every polygon of a map with deadly-liquid flags (magma 0x2 or slime 0x4).
import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

type poly struct {
	Flags uint16       `json:"f"`
	Verts [][3]float64 `json:"v"`
}

func main() {
	mm := flag.String("mmaps", "", "mmaps dir")
	m := flag.Uint("map", 0, "map id")
	out := flag.String("out", "", "output JSON")
	flag.Parse()
	pl := navmesh.NewPlanner(*mm)
	defer pl.Close()
	polys, trunc, err := pl.Polygons(uint32(*m), -20000, -20000, 20000, 20000)
	if err != nil {
		panic(err)
	}
	var res []poly
	for _, p := range polys {
		if p.Flags&(2|4) == 0 {
			continue
		}
		q := poly{Flags: p.Flags}
		for _, v := range p.Verts {
			q.Verts = append(q.Verts, [3]float64{float64(v[0]), float64(v[1]), float64(v[2])})
		}
		res = append(res, q)
	}
	b, _ := json.Marshal(res)
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		panic(err)
	}
	fmt.Println("polygons", len(res), "truncated", trunc)
}
