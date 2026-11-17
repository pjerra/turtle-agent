package session

import (
	"bytes"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
)

func TestRedactorScrub(t *testing.T) {
	r := newRedactor([]string{"Harness", "", "HARNESS"}, []string{"pw-secret", ""})
	for in, want := range map[string]string{
		"":                          "",
		"nothing here":              "nothing here",
		"Account: HARNESS (id 5)":   "Account: <redacted> (id 5)",
		"harness and hArNeSs":       "<redacted> and <redacted>",
		"x pw-secret,harness y":     "x <redacted>,<redacted> y",
		"Harnessb is the character": "Harnessb is the character", // a name made from the account stays
		"xharness harness2":         "xharness harness2",
		"xpw-secretx":               "x<redacted>x", // a password matches anywhere
		"Hunter2pw-SECRETs":         "Hunter2<redacted>s",
		// A colour escape right before the account name is a word boundary (LANG_SYSTEMMESSAGE 3).
		"|cffffff00HARNESS logged in|r": "|cffffff00<redacted> logged in|r",
		"|cff00ff00HARNESS|r":           "|cff00ff00<redacted>|r",
		"|CFF00FF00harness|r":           "|CFF00FF00<redacted>|r",
		"|cff00ff0zHARNESS|r":           "|cff00ff0zHARNESS|r", // not an escape: z is no hex digit
		"x|cff00ff00HARNESSb":           "x|cff00ff00HARNESSb",
		// So are a colour reset and a link's text marker (review: data.raw and addon text keep them).
		"Account:|rHARNESS (id 5)":     "Account:|r<redacted> (id 5)",
		"|Hplayer:HARNESS|hHARNESS|h":  "|Hplayer:<redacted>|h<redacted>|h",
		"|RHARNESS and |HHARNESS":      "|R<redacted> and |H<redacted>",
		"x|nHARNESS":                   "x|n<redacted>",
		"x|rHARNESSb":                  "x|rHARNESSb",
		"rHARNESS hHARNESS":            "rHARNESS hHARNESS", // no pipe: a plain word
		"'harness's'":                  "'<redacted>'s'",
		"(HARNESS)":                    "(<redacted>)",
		"PW-SECRET in upper case":      "<redacted> in upper case",
		"ünïcödé harness ünïcödé":      "ünïcödé <redacted> ünïcödé",
		"harnes s is not a full match": "harnes s is not a full match",
	} {
		if got := r.scrub(in); got != want {
			t.Errorf("scrub(%q) = %q, want %q", in, got, want)
		}
	}
	if len(r.needles) != 2 || r.needles[0].anywhere || !r.needles[1].anywhere {
		t.Fatalf("needles %+v", r.needles)
	}
	if !r.contains([]byte("..HaRnEsS..")) || r.contains([]byte("harnes")) || (redactor{}).contains([]byte("harness")) {
		t.Fatal("contains")
	}
}

// Review I3: an account name the server echoes (a .pinfo reply) never reaches the event
// log, and a packet holding it is never hex-dumped to the stderr log.
func TestEchoedAccountNameIsRedacted(t *testing.T) {
	var logBuf bytes.Buffer
	h := start(t, Options{Logger: &logBuf, Redact: []string{"TESTACC"}, RedactAnywhere: []string{"pw-secret"}})
	h.enterWorld()
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatSystem, 0, "Player Harnessb (guid 42) Account: testacc (id 5) GMLevel: 3"))
	h.send(opcode.SMSGNotification, []byte("TestAcc pw-secret")) // no terminating NUL: a parse error
	h.logout()
	page := h.s.Log().Read(events.Query{Limit: events.MaxLimit})
	var all strings.Builder
	for _, e := range page.Events {
		all.WriteString(e.Text + e.Source + e.Target)
		for _, v := range e.Data {
			if s, ok := v.(string); ok {
				all.WriteString(s)
			}
		}
	}
	for _, secret := range []string{"testacc", "TESTACC", "TestAcc", "pw-secret"} {
		if strings.Contains(all.String(), secret) || strings.Contains(logBuf.String(), secret) {
			t.Errorf("%q leaked: events %q log %q", secret, all.String(), logBuf.String())
		}
	}
	if sys := ofKind(h.s.Log(), events.KindSystem); len(sys) != 1 || sys[0].Text != "Player Harnessb (guid 42) Account: <redacted> (id 5) GMLevel: 3" {
		t.Fatalf("system %+v", sys)
	}
	if !strings.Contains(logBuf.String(), "SMSG_NOTIFICATION") || !strings.Contains(logBuf.String(), "no dump") || strings.Contains(logBuf.String(), "00000000") {
		t.Fatalf("log %q", logBuf.String())
	}
}

// Review fix: the raw line (data.raw) and addon text keep "|r" and "|h…|h" escapes, which
// must not hide an account name from the whole-word match.
func TestRawAndAddonTextAreRedacted(t *testing.T) {
	h := start(t, Options{Redact: []string{"TESTACC"}})
	h.enterWorld()
	h.send(opcode.SMSGMessagechat, gametest.ChatPacket(game.ChatSystem, 0, 0, "", 0, "",
		"|cff00ff00Account:|r|Hplayer:TESTACC|hTESTACC|h (id 5)", 0))
	h.send(opcode.SMSGMessagechat, gametest.AddonMessage(me.GUID, "TBM", "INFO|rTESTACC|hTestAcc", 0))
	h.logout()
	sys := ofKind(h.s.Log(), events.KindSystem)
	if len(sys) != 1 || sys[0].Data["raw"] != "|cff00ff00Account:|r|Hplayer:<redacted>|h<redacted>|h (id 5)" ||
		sys[0].Text != "Account:<redacted> (id 5)" {
		t.Fatalf("system %+v", sys)
	}
	if a := ofKind(h.s.Log(), events.KindAddon); len(a) != 1 || a[0].Text != "INFO|r<redacted>|h<redacted>" {
		t.Fatalf("addon %+v", a)
	}
}
