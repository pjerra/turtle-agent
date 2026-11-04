package slots

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstanceCaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, InstanceCapPerAccountFile)
	if caps, err := LoadInstanceCaps(path); err != nil || caps != nil {
		t.Fatalf("missing file: caps %v err %v, want nil nil", caps, err)
	}
	if err := os.WriteFile(path, []byte(`{"m910q":30,"tortoise-test2":30}`), 0o600); err != nil {
		t.Fatal(err)
	}
	caps, err := LoadInstanceCaps(path)
	if err != nil || caps["m910q"] != 30 || caps["tortoise-test2"] != 30 {
		t.Fatalf("caps %v err %v", caps, err)
	}
	if err := os.WriteFile(path, []byte(`{"m910q":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstanceCaps(path); err == nil || !strings.Contains(err.Error(), "1 or more") {
		t.Fatalf("zero cap: %v, want a refusal", err)
	}
}
