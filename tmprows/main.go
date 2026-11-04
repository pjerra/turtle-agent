package main

import (
	"fmt"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

func main() {
	p, err := dungeon.Load("dungeons/zul_gurub.json")
	if err != nil { panic(err) }
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil { panic(err) }
	fmt.Println("steps", len(res.Rows.Steps))
	for i, s := range res.Rows.Steps {
		if i+1 >= 530 && i+1 <= 700 {
			fmt.Printf("%d leg=%d kind=%s entry=%d entries=%v comment=%q\n", i+1, s.Leg, s.Kind, s.Entry, s.Entries, s.Comment)
		}
	}
}
