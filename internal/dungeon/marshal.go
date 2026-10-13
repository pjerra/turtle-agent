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
	b = numberLists.ReplaceAllFunc(b, func(m []byte) []byte {
		inner := bytes.Join(bytes.Fields(bytes.Trim(m, "[]")), nil)
		return append(append([]byte("["), bytes.ReplaceAll(inner, []byte(","), []byte(", "))...), ']')
	})
	return append(bytes.TrimRight(b, "\n"), '\n'), nil
}
