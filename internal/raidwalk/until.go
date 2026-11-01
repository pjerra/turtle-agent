package raidwalk

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

// UntilBossDone is the --until word that stops the walk after the first boss step has completed (--until boss stops before it). The run
// leader's own until has no such clause, so it is resolved against the plan's rows to "step <the step after the boss>" (BossDoneUntil).
const UntilBossDone = "boss-done"

// IsBossDone says whether s is the boss-done word.
func IsBossDone(s string) bool { return strings.TrimSpace(s) == UntilBossDone }

// BossDoneUntil resolves boss-done against a plan's rows: the clause for ParseUntil ("step N", N being the step after the first boss
// step; "none" when the boss is the plan's last step, which then runs to its end) and the boss step's number (1-based, the run leader's
// own numbering). The rows must be the ones the server runs: the caller compares the step count with the one the server shows.
func BossDoneUntil(rows planexport.Rows) (clause string, bossStep int, err error) {
	for i, st := range rows.Steps {
		if st.Kind != planexport.KindBoss {
			continue
		}
		bossStep = i + 1
		if bossStep == len(rows.Steps) {
			return "none", bossStep, nil
		}
		return fmt.Sprintf("step %d", bossStep+1), bossStep, nil
	}
	return "", 0, errors.New("--until boss-done: the plan has no boss step")
}

var (
	untilStepRe = regexp.MustCompile(`^[1-9][0-9]*$`)
	untilLegRe  = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// ParseUntil parses --until: boss, none, "step N" or "leg L" (boss-done is resolved to one of these first, BossDoneUntil). It returns the words the run start carries and the
// clause as the server writes it in the status label ("until boss", "until step 29", "until leg gehennas").
func ParseUntil(s string) ([]string, string, error) {
	fields := strings.Fields(s)
	switch {
	case len(fields) == 1 && fields[0] == "boss":
		return []string{"until", "boss"}, "until boss", nil
	case len(fields) == 1 && fields[0] == "none":
		return nil, "", nil
	case len(fields) == 2 && fields[0] == "step":
		if !untilStepRe.MatchString(fields[1]) {
			return nil, "", fmt.Errorf("--until step wants a step number from 1, not %q", fields[1])
		}
		return []string{"until", "step", fields[1]}, "until step " + fields[1], nil
	case len(fields) == 2 && fields[0] == "leg":
		if !untilLegRe.MatchString(fields[1]) {
			return nil, "", fmt.Errorf("--until leg wants a leg name or number, not %q", fields[1])
		}
		return []string{"until", "leg", fields[1]}, "until leg " + fields[1], nil
	}
	if IsBossDone(s) {
		return nil, "", errors.New("--until boss-done needs the plan's rows: resolve it with BossDoneUntil first")
	}
	return nil, "", fmt.Errorf("--until wants boss, boss-done, none, \"step N\" or \"leg L\", not %q", s)
}

// ParseFrom parses --from: "leg L" or "step N". A bare number is refused: the server's run start takes the word.
func ParseFrom(s string) ([]string, error) {
	fields := strings.Fields(s)
	switch {
	case len(fields) == 2 && fields[0] == "step":
		if !untilStepRe.MatchString(fields[1]) {
			return nil, fmt.Errorf("--from step wants a step number from 1, not %q", fields[1])
		}
		return []string{"from", "step", fields[1]}, nil
	case len(fields) == 2 && fields[0] == "leg":
		if !untilLegRe.MatchString(fields[1]) {
			return nil, fmt.Errorf("--from leg wants a leg name or number, not %q", fields[1])
		}
		return []string{"from", "leg", fields[1]}, nil
	}
	return nil, fmt.Errorf("--from wants \"leg L\" or \"step N\", not %q", s)
}
