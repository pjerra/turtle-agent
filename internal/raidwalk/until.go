package raidwalk

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	untilStepRe = regexp.MustCompile(`^[1-9][0-9]*$`)
	untilLegRe  = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// ParseUntil parses --until: boss, none, "step N" or "leg L". It returns the words the run start carries and the
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
	return nil, "", fmt.Errorf("--until wants boss, none, \"step N\" or \"leg L\", not %q", s)
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
