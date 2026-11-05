package dungeon

import (
	"strings"
	"testing"
)

const proofUse = `{"version":1,"name":"Proof_Temple","status":"live-checked (test)","map":109,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"statues","to":{"x":-510,"y":95,"z":-148},
  "steps":[{"use_object":{"entry":148830,"near":{"x":-515.553,"y":95.2582,"z":-148.74},"proof":{"gameobject":148883,"radius":10}}}],
  "boss":{"entry":8580,"position":{"x":-460,"y":96.8,"z":-189.58}}}]}}`

func TestUseObjectProofParses(t *testing.T) {
	p, err := Parse("test.json", []byte(proofUse))
	if err != nil {
		t.Fatal(err)
	}
	u := p.Legs[0].Steps[0].UseObject
	if u == nil || u.Proof == nil {
		t.Fatalf("the use_object has no proof: %+v", u)
	}
	if *u.Proof != (UseProof{GameObject: 148883, Radius: 10}) {
		t.Fatalf("proof %+v", u.Proof)
	}
}

func TestUseObjectProofRefusals(t *testing.T) {
	use := func(proof string) string {
		return `{"version":1,"name":"P","status":"live-checked (test)","map":109,"entrance":{"x":0,"y":0,"z":0},"legs":[` +
			`{"name":"s","to":{"x":-510,"y":95,"z":-148},"steps":[{"use_object":{"entry":148830,` +
			`"near":{"x":-515.553,"y":95.2582,"z":-148.74},"stand":{"x":-513,"y":95.3,"z":-148.74}` + proof + `}}],` +
			`"boss":{"entry":8580,"position":{"x":-460,"y":96.8,"z":-189.58}}}]}}`
	}
	cases := []struct {
		name string
		plan string
		want string
	}{
		{"both entries", use(`,"proof":{"gameobject":148883,"creature":8580,"radius":10}`), ".use_object.proof: name exactly one of gameobject and creature"},
		{"neither entry", use(`,"proof":{"radius":10}`), ".use_object.proof: name exactly one of gameobject and creature"},
		{"radius zero", use(`,"proof":{"gameobject":148883,"radius":0}`), ".use_object.proof.radius: 0: more than 0 and at most 20 yards around near"},
		{"radius over", use(`,"proof":{"gameobject":148883,"radius":21}`), ".use_object.proof.radius: 21: more than 0 and at most 20 yards around near"},
		{"via item", use(`,"requires_item":{"entry":9240},"via_item":true,"proof":{"gameobject":148883,"radius":10}`), ".use_object.proof: a via_item use is the run leader's use_on, which takes no proof"},
		{"proof is the object", use(`,"proof":{"gameobject":148830,"radius":10}`), ".use_object.proof.gameobject: 148830 is the used object itself: the proof is another object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("test.json", []byte(tc.plan))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v lacks %q", err, tc.want)
			}
		})
	}
	// Without near the proof has no point to search around.
	noNear := `{"version":1,"name":"P","status":"live-checked (test)","map":109,"entrance":{"x":0,"y":0,"z":0},"legs":[` +
		`{"name":"s","to":{"x":-510,"y":95,"z":-148},"steps":[{"use_object":{"entry":148830,"proof":{"gameobject":148883,"radius":10}}}],` +
		`"boss":{"entry":8580,"position":{"x":-460,"y":96.8,"z":-189.58}}}]}}`
	if _, err := Parse("test.json", []byte(noNear)); err == nil ||
		!strings.Contains(err.Error(), ".use_object.proof: needs the object's entry and near") {
		t.Fatalf("error %v lacks the near path", err)
	}
}
