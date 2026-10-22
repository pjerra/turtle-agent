package main

import (
	"regexp"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/opcode"
)

// ttSent is the line the method sends: the command as given (the client adds nothing but the request id it made up).
var ttSent = regexp.MustCompile(`\.tt q floor -900 -2300 92\.5 #([0-9a-z]+)\x00`)

// The `tt` method sends one `.tt` line and answers with the replies carrying its own request id: a reply of another id (an older
// answer) is dropped, as in the start path's client.
func TestTTMethod(t *testing.T) {
	r := newRig(t)
	got := r.callAsync("tt", `{"cmd":"q floor -900 -2300 92.5"}`)
	p, err := r.srv.Expect(opcode.CMSGMessagechat, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	m := ttSent.FindStringSubmatch(string(p.Body))
	if m == nil {
		t.Fatalf("sent %q", p.Body)
	}
	id := m[1]
	r.system(
		`TTJSON {"v":1,"cmd":"q.floor","id":"old","ok":true,"floor":1}`,
		`TTJSON {"v":1,"cmd":"q.floor","id":"`+id+`","ok":true,"floor":91.67,"top":91.67,"multi":false}`,
	)
	res := resultOf(t, <-got)
	replies := res["replies"].([]any)
	if len(replies) != 1 {
		t.Fatalf("replies %v", res["replies"])
	}
	if floor := replies[0].(map[string]any)["floor"]; floor != 91.67 {
		t.Fatalf("reply %v", replies[0])
	}
}

// A refused request is an RPC error carrying the toolkit's code; a bad cmd is refused before anything is sent.
func TestTTMethodErrors(t *testing.T) {
	r := newRig(t)
	got := r.callAsync("tt", `{"cmd":"q unit 0x1"}`)
	p, err := r.srv.Expect(opcode.CMSGMessagechat, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`#([0-9a-z]+)\x00`).FindStringSubmatch(string(p.Body))
	if m == nil {
		t.Fatalf("sent %q", p.Body)
	}
	r.system(`TTJSON {"v":1,"cmd":"q.unit","id":"` + m[1] + `","ok":false,"err":"not_allowed","msg":"rank 3 needed"}`)
	out := <-got
	e, ok := out["error"].(map[string]any)
	if !ok || e["code"] != float64(-32003) || e["data"].(map[string]any)["err"] != "not_allowed" {
		t.Fatalf("%v", out)
	}

	for _, bad := range []string{`{"cmd":""}`, `{"cmd":"q floor 1 2 3 #abc"}`, `{"cmd":"q floor\n1 2 3"}`} {
		out := r.call("tt", bad)
		if e, ok := out["error"].(map[string]any); !ok || e["code"] != float64(-32602) {
			t.Fatalf("%s: %v", bad, out)
		}
	}
	if _, err := r.srv.Expect(opcode.CMSGMessagechat, 200*time.Millisecond); err == nil {
		t.Fatal("a refused cmd was sent")
	}
}
