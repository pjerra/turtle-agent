package routeedit

import (
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	if UnifiedDiff("a", "b", "x\ny\n", "x\ny\n") != "" {
		t.Error("equal texts have no diff")
	}
	var a, b []string
	for i := 1; i <= 30; i++ {
		a = append(a, "line "+string(rune('a'+i%26))+string(rune('0'+i%10)))
	}
	b = append(b, a...)
	b[4] = "CHANGED five"
	b = append(b[:20], append([]string{"inserted"}, b[20:]...)...)
	d := UnifiedDiff("old", "new", strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n")
	for _, want := range []string{"--- old\n+++ new\n", "-" + a[4] + "\n+CHANGED five\n", "+inserted\n", "@@ -2,7 +2,7 @@\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	if strings.Count(d, "@@") != 4 { // two hunks, two markers each
		t.Errorf("want two hunks:\n%s", d)
	}
	// lines far from a change are not in the diff
	if strings.Contains(d, a[12]) {
		t.Errorf("a line 8 away from any change is in the diff:\n%s", d)
	}
	// an addition at the end and a removal at the start
	d = UnifiedDiff("a", "b", "1\n2\n3\n", "2\n3\n4\n")
	if !strings.Contains(d, "-1\n") || !strings.Contains(d, "+4\n") || !strings.Contains(d, " 2\n 3\n") {
		t.Errorf("%s", d)
	}
	// one side empty
	if d := UnifiedDiff("a", "b", "", "x\n"); !strings.Contains(d, "+x\n") {
		t.Errorf("%s", d)
	}
}
