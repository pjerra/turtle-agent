package scenario

import (
	"regexp"
	"testing"
)

// TestExpandQuotesValuesInMatch pins that a var value is regex-quoted inside a
// match (a name with a dot must not match anything) and verbatim elsewhere,
// and that $$ is a literal dollar.
func TestExpandQuotesValuesInMatch(t *testing.T) {
	vars := map[string]string{"a": "a.b", "gid": "7"}
	got, err := expand("x${a}y", vars, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != `xa\.by` {
		t.Fatalf("got %q", got)
	}
	re := regexp.MustCompile(got)
	if re.MatchString("xay") || re.MatchString("xaxb") || !re.MatchString("xa.by") {
		t.Fatalf("quoted %q matches wrongly", got)
	}
	got, err = expand("give ${gid} $${a}", vars, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "give 7 ${a}" {
		t.Fatalf("got %q", got)
	}
	if _, err := expand("${missing}", vars, false); err == nil {
		t.Fatal("unset var expanded")
	}
}
