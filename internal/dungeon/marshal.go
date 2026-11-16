package dungeon

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// A position is written on one line, whatever the indentation around it.
var pointLines = regexp.MustCompile(`\{\s*"x": ([^,\s]+),\s*"y": ([^,\s]+),\s*"z": ([^,}\s]+)\s*\}`)

// An item reference too.
var itemLines = regexp.MustCompile(`\{\s*"entry": (\d+),\s*"name": ("[^"]*")\s*\}`)
var bareItemLines = regexp.MustCompile(`\{\s*"entry": (\d+)\s*\}`)

// A leg's optional item is too (Leg.Optional), and its also entries are one line each.
var optionalLines = regexp.MustCompile(`\{\s*"item": (\d+),\s*"name": ("[^"]*")\s*\}`)
var bareOptionalLines = regexp.MustCompile(`\{\s*"item": (\d+)\s*\}`)
var alsoLines = regexp.MustCompile(`\{\s*"item": (\d+),\s*"count": (\d+),\s*"name": ("[^"]*")\s*\}`)
var alsoNoNameLines = regexp.MustCompile(`\{\s*"item": (\d+),\s*"count": (\d+)\s*\}`)

// A list of entries is too.
var numberLists = regexp.MustCompile(`\[[\d,\s]+\]`)

// Marshal writes p as indented JSON with each position on one line, the way plan files are kept and diffed.
func Marshal(p *Plan) ([]byte, error) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	b = pointLines.ReplaceAll(b, []byte(`{"x": $1, "y": $2, "z": $3}`))
	b = itemLines.ReplaceAll(b, []byte(`{"entry": $1, "name": $2}`))
	b = bareItemLines.ReplaceAll(b, []byte(`{"entry": $1}`))
	b = optionalLines.ReplaceAll(b, []byte(`{"item": $1, "name": $2}`))
	b = bareOptionalLines.ReplaceAll(b, []byte(`{"item": $1}`))
	b = alsoLines.ReplaceAll(b, []byte(`{"item": $1, "count": $2, "name": $3}`))
	b = alsoNoNameLines.ReplaceAll(b, []byte(`{"item": $1, "count": $2}`))
	b = numberLists.ReplaceAllFunc(b, func(m []byte) []byte {
		inner := bytes.Join(bytes.Fields(bytes.Trim(m, "[]")), nil)
		return append(append([]byte("["), bytes.ReplaceAll(inner, []byte(","), []byte(", "))...), ']')
	})
	return append(bytes.TrimRight(b, "\n"), '\n'), nil
}
