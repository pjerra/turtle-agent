package main

// pointprobe: for each point, the navmesh floors near it and the polygons within a radius, with flags.
// usage: pointprobe -mmaps DIR -map 816 -in points.json [-dz 4] [-rad 3]
import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

type pt struct {
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
	Z  float64 `json:"z"`
	ID string  `json:"id"`
}

func distSeg(p, a, b navmesh.Vec3) float64 {
	// distance from p to segment ab in 3D
	ax, ay, az := float64(b[0]-a[0]), float64(b[1]-a[1]), float64(b[2]-a[2])
	px, py, pz := float64(p[0]-a[0]), float64(p[1]-a[1]), float64(p[2]-a[2])
	den := ax*ax + ay*ay + az*az
	t := 0.0
	if den > 0 {
		t = (px*ax + py*ay + pz*az) / den
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
	}
	dx, dy, dz := px-ax*t, py-ay*t, pz-az*t
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func main() {
	mm := flag.String("mmaps", "", "mmaps dir")
	m := flag.Uint("map", 0, "map id")
	in := flag.String("in", "", "JSON file: [{x,y,z,id}]")
	dz := flag.Float64("dz", 4, "floor window half-height")
	rad := flag.Float64("rad", 3, "polygon radius of interest")
	flag.Parse()
	var pts []pt
	b, err := os.ReadFile(*in)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(b, &pts); err != nil {
		panic(err)
	}
	pl := navmesh.NewPlanner(*mm)
	defer pl.Close()
	mapID := uint32(*m)
	for _, q := range pts {
		p := navmesh.Vec3{float32(q.X), float32(q.Y), float32(q.Z)}
		fl, err := pl.Floors(mapID, p[0], p[1], p[2]-float32(*dz), p[2]+float32(*dz))
		out := map[string]any{"id": q.ID, "x": q.X, "y": q.Y, "z": q.Z}
		if err != nil {
			out["error"] = err.Error()
		}
		var fs []string
		under := false
		for _, f := range fl {
			fs = append(fs, fmt.Sprintf("%.2f/0x%x", f.Z, f.Flags))
			if math.Abs(float64(f.Z-p[2])) <= *dz {
				under = true
			}
		}
		out["floors"] = fs
		out["under"] = under
		// polygons near
		polys, trunc, err := pl.Polygons(mapID, p[0]-float32(*rad), p[1]-float32(*rad), p[0]+float32(*rad), p[1]+float32(*rad))
		if err != nil {
			out["polyError"] = err.Error()
		}
		out["trunc"] = trunc
		near := []map[string]any{}
		for _, poly := range polys {
			if len(poly.Verts) < 3 {
				continue
			}
			best := math.Inf(1)
			for i := 0; i < len(poly.Verts); i++ {
				if d := distSeg(p, poly.Verts[i], poly.Verts[(i+1)%len(poly.Verts)]); d < best {
					best = d
				}
			}
			if best <= *rad {
				zmin, zmax := math.Inf(1), math.Inf(-1)
				for _, v := range poly.Verts {
					if z := float64(v[2]); z < zmin {
						zmin = z
					} else if z > zmax {
						zmax = z
					}
				}
				if zmax < zmin {
					zmax = zmin
				}
				near = append(near, map[string]any{"flags": poly.Flags, "d": math.Round(best*100) / 100,
					"zmin": math.Round(zmin*100) / 100, "zmax": math.Round(zmax*100) / 100})
			}
		}
		out["near"] = near
		if np, ok, err := pl.Nearest(mapID, p); err != nil {
			out["nearestErr"] = err.Error()
		} else if ok {
			out["nearest"] = []float64{math.Round(float64(np[0])*1000) / 1000, math.Round(float64(np[1])*1000) / 1000, math.Round(float64(np[2])*1000) / 1000}
			out["nearestD"] = math.Round(1000*distSeg(p, np, np)) / 1000
		} else {
			out["nearest"] = nil
		}
		j, _ := json.Marshal(out)
		fmt.Println(string(j))
	}
}
