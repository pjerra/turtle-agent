package main

// floorsFlag is set when -floors is given: print the navmesh surfaces at each
// "-at x,y,lo,hi" point instead of replaying rows.

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

func printFloors(pl *navmesh.Planner, mapID uint32, at [][4]float32) {
	for _, a := range at {
		fl, err := pl.Floors(mapID, a[0], a[1], a[2], a[3])
		if err != nil {
			fmt.Printf("%.2f %.2f: error %v\n", a[0], a[1], err)
			continue
		}
		fmt.Printf("%.2f %.2f [%.1f..%.1f]:", a[0], a[1], a[2], a[3])
		for _, f := range fl {
			fmt.Printf(" z%.2f(0x%x)", f.Z, f.Flags)
		}
		fmt.Println()
	}
}
