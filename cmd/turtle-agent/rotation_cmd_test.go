package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotationCommand(t *testing.T) {
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := rotationCmd(args, &out, &errb)
		return code, out.String(), errb.String()
	}
	code, out, _ := run("list")
	if code != exitOK || !strings.Contains(out, "frost_mage") || !strings.Contains(out, "arms_warrior") || !strings.Contains(out, "replaces mage_frost") {
		t.Fatalf("%d %s", code, out)
	}
	code, out, _ = run("show", "--level", "44", "frost_mage")
	if code != exitOK || !strings.Contains(out, "frostbolt") || !strings.Contains(out, "level 44: id 10179 rank 8") {
		t.Fatalf("%d %s", code, out)
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(good, []byte(`{"version":1,"name":"mine","class":"mage","replaces":["mage_frost"],"priorities":[{"id":"bolt","spell":"Frostbolt","level":4}]}`), 0o600)
	os.WriteFile(bad, []byte(`{"version":1,"name":"mine","class":"mage","replaces":["mage_frost"],"priorities":[{"id":"bolt","spell":"Frostbolt","level":9}]}`), 0o600)
	if code, out, _ = run("check", good); code != exitOK || !strings.Contains(out, "good.json: ok") {
		t.Fatalf("%d %s", code, out)
	}
	if code, _, errs := run("check", good, bad); code == exitOK || !strings.Contains(errs, "bad.json") || !strings.Contains(errs, "learned at level") {
		t.Fatalf("%d %s", code, errs)
	}
	if code, out, _ = run("show", good); code != exitOK || !strings.Contains(out, "rotation mine") {
		t.Fatalf("a file by path: %d %s", code, out)
	}
	for _, args := range [][]string{{}, {"nope"}, {"show"}, {"check"}, {"show", "no_such_rotation"}} {
		if code, _, _ := run(args...); code == exitOK {
			t.Errorf("%v was accepted", args)
		}
	}
}
