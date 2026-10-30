package plandiff

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/minimap"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

func TestSVGMarksAndCoordinates(t *testing.T) {
	pic := &minimap.Meta{Map: 249, Width: 200, Height: 100, PxPerYard: 2, North: 100, South: 0, West: 50, East: 0}
	oldRow := planexport.StepRow{Plan: "p", Leg: 0, Seq: 25, Kind: "move", X: 10, Y: 20, Z: -74.6, Comment: "path"}
	newRow := oldRow
	newRow.Y, newRow.Z, newRow.Comment = 21, -66.7, "boss approach"
	oldRows := planexport.Rows{Plan: planexport.PlanRow{Plan: "p", Map: 249}, Steps: []planexport.StepRow{oldRow}}
	newRows := planexport.Rows{Plan: planexport.PlanRow{Plan: "p", Map: 249}, Steps: []planexport.StepRow{newRow}}
	d := Diff{Plan: "p", Map: 249, Changes: []Change{{
		Type: TypeMoved, Leg: "0", OldStep: 26, NewStep: 26, OldSeq: 25, NewSeq: 25, Kind: "move",
		Old: &oldRow, New: &newRow, DeltaYd: 7.92, Fields: []string{"comment"},
	}}}
	svg := string(SVG(d, oldRows, newRows, pic, nil))
	if !strings.Contains(svg, `class="moved"`) {
		t.Fatalf("no moved marker:\n%s", svg)
	}
	// ToPixel(10, 20) = ((50-20)*2, (100-10)*2) = (60, 180); ToPixel(10, 21) = (58, 180).
	if !strings.Contains(svg, `x1="60.0" y1="180.0" x2="58.0" y2="180.0"`) {
		t.Errorf("arrow ends not at ToPixel:\n%s", svg)
	}
	if !strings.Contains(svg, `>26 z -66.7<`) && !strings.Contains(svg, `>26 z -66.7 <`) {
		t.Errorf("no label with the new step number:\n%s", svg)
	}
	if !strings.Contains(svg, `width="200" height="100"`) || !strings.Contains(svg, `viewBox="0 0 200 100"`) {
		t.Errorf("canvas not the picture's:\n%s", svg)
	}
	if !strings.Contains(svg, `class="old"`) || !strings.Contains(svg, `class="new"`) {
		t.Errorf("routes missing:\n%s", svg)
	}
}

func TestSVGZMovedStillMarked(t *testing.T) {
	oldRow := planexport.StepRow{Plan: "p", Leg: 0, Seq: 25, Kind: "move", X: 10, Y: 20, Z: -74.6, Comment: "path"}
	newRow := oldRow
	newRow.Z = -66.7
	rows := planexport.Rows{Plan: planexport.PlanRow{Plan: "p", Map: 249}, Steps: []planexport.StepRow{oldRow}}
	newRows := planexport.Rows{Plan: planexport.PlanRow{Plan: "p", Map: 249}, Steps: []planexport.StepRow{newRow}}
	d := Diff{Plan: "p", Map: 249, Changes: []Change{{Type: TypeMoved, Leg: "0", OldStep: 26, NewStep: 26, Old: &oldRow, New: &newRow, DeltaYd: 7.92}}}
	svg := string(SVG(d, rows, newRows, nil, nil))
	if !strings.Contains(svg, `class="moved"`) || !strings.Contains(svg, `>26 z -66.7<`) {
		t.Errorf("z-only move unmarked:\n%s", svg)
	}
}

func TestSVGEmbedsPicture(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nnot really a png")
	rows := planexport.Rows{
		Plan:  planexport.PlanRow{Plan: "p", Map: 1},
		Legs:  []planexport.LegRow{{Plan: "p", Leg: 0, Name: "a"}},
		Steps: []planexport.StepRow{{Plan: "p", Leg: 0, Seq: 0, Kind: "move", X: 5, Y: 5, Z: 1}},
	}
	svg := string(SVG(Diff{Plan: "p", Map: 1}, rows, rows, nil, png))
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if !strings.Contains(svg, "<image") || !strings.Contains(svg, want) {
		t.Fatalf("picture not embedded:\n%s", svg)
	}
	if strings.Contains(svg, `width="0"`) || strings.Contains(svg, `viewBox="0 0 0`) {
		t.Errorf("no bounds for a blank canvas:\n%s", svg)
	}
}

func TestSVGEscapesText(t *testing.T) {
	added := planexport.StepRow{Plan: "p", Leg: 0, Seq: 1, Kind: "move", X: 5, Y: 5, Z: 1, Comment: `<script>alert("x")</script>`}
	rows := planexport.Rows{Plan: planexport.PlanRow{Plan: "p", Map: 1}, Steps: []planexport.StepRow{added}}
	d := Diff{Plan: "p", Map: 1, Changes: []Change{{Type: TypeAdded, Leg: "0", NewStep: 1, NewSeq: 1, Kind: "move", New: &added}}}
	svg := string(SVG(d, planexport.Rows{}, rows, nil, nil))
	if strings.Contains(svg, "<script>") {
		t.Fatalf("unescaped text:\n%s", svg)
	}
	if !strings.Contains(svg, "&lt;script&gt;") {
		t.Errorf("escaped text missing:\n%s", svg)
	}
}

func TestSVGTwoFloorDesc(t *testing.T) {
	rows := planexport.Rows{Steps: []planexport.StepRow{{Kind: "move", X: 5, Y: 5, Z: 1}}}
	plain := string(SVG(Diff{}, rows, rows, nil, nil))
	if strings.Contains(plain, "multi-floor") {
		t.Errorf("desc without the 2f picture:\n%s", plain)
	}
	two := string(SVG2F(Diff{}, rows, rows, nil, nil))
	if !strings.Contains(two, "<desc>2D overlay: z decides on multi-floor maps</desc>") {
		t.Errorf("no desc:\n%s", two)
	}
}
