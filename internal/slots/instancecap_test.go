package slots

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstanceCaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, InstanceCapPerAccountFile)
	if caps, err := LoadInstanceCaps(path); err != nil || caps != nil {
		t.Fatalf("missing file: caps %v, err %v", caps, err)
	}
	if err := os.WriteFile(path, []byte(`{"m910q": 30, "tortoise-test2": 30}`), 0o600); err != nil {
		t.Fatal(err)
	}
	caps, err := LoadInstanceCaps(path)
	if err != nil {
		t.Fatal(err)
	}
	if caps["m910q"] != 30 || caps["tortoise-test2"] != 30 {
		t.Fatalf("caps %v", caps)
	}
	for _, bad := range []string{`{"m910q": 0}`, `{"m910q": -1}`, `not json`} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadInstanceCaps(path); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestEffectiveInstanceCap(t *testing.T) {
	caps := map[string]int{"m910q": 30}
	cases := []struct {
		name     string
		explicit int
		server   string
		want     int
	}{
		{"the flag wins", 12, "m910q", 12},
		{"the file", 0, "m910q", 30},
		{"the stock", 0, "other", StockInstanceCap},
		{"no file", 0, "m910q", StockInstanceCap},
	}
	for _, c := range cases {
		m := caps
		if c.name == "no file" {
			m = nil
		}
		if got := EffectiveInstanceCap(c.explicit, m, c.server); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
