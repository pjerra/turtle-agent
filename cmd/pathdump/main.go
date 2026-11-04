package main

// Scratch tool (not committed): prints the server-exact path points for one or more hops.
// args: mmaps map x y z x y z [x y z x y z ...]
import (
	"fmt"
	"os"
	"strconv"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

func main() {
	pl := navmesh.NewPlanner(os.Args[1])
	m, _ := strconv.Atoi(os.Args[2])
	a := os.Args[3:]
	for i := 0; i+6 <= len(a); i += 6 {
		var v [6]float32
		for k := 0; k < 6; k++ {
			f, _ := strconv.ParseFloat(a[i+k], 32)
			v[k] = float32(f)
		}
		from := navmesh.Vec3{v[0], v[1], v[2]}
		to := navmesh.Vec3{v[3], v[4], v[5]}
		sp, err := pl.ServerPath(uint32(m), from, to)
		if err != nil {
			fmt.Println("ERR", err)
			continue
		}
		fmt.Printf("HOP %v -> %v type 0x%x verdict %s len %.1f end %.1f %.1f %.1f\n", from, to, sp.Type, navmesh.ServerMoveVerdict(sp, from, to, 0), sp.Length, sp.ActualEnd[0], sp.ActualEnd[1], sp.ActualEnd[2])
		for _, p := range sp.Points {
			fmt.Printf("P %.2f %.2f %.2f\n", p[0], p[1], p[2])
		}
	}
}
