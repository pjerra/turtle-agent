package scenario

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// expand replaces ${name} in s with vars. In a match the value is
// regexp.QuoteMeta'd so a name with a dot cannot match anything; elsewhere it
// goes in as is. $$ is a literal dollar; a $ not followed by { or $ stays.
func expand(s string, vars map[string]string, regexQuote bool) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			b.WriteByte('$')
			i++
			continue
		}
		switch s[i+1] {
		case '$':
			b.WriteByte('$')
			i += 2
		case '{':
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteString(s[i:])
				i = len(s)
				continue
			}
			name := s[i+2 : i+2+end]
			v, ok := vars[name]
			if !ok {
				return "", fmt.Errorf("scenario: ${%s} is not set", name)
			}
			if regexQuote {
				v = regexp.QuoteMeta(v)
			}
			b.WriteString(v)
			i += 2 + end + 1
		default:
			b.WriteByte('$')
			i++
		}
	}
	return b.String(), nil
}

// expandAny expands every string inside v recursively.
func expandAny(v any, vars map[string]string) (any, error) {
	switch t := v.(type) {
	case string:
		return expand(t, vars, false)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			x, err := expandAny(e, vars)
			if err != nil {
				return nil, err
			}
			out[k] = x
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			x, err := expandAny(e, vars)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	default:
		return v, nil
	}
}

// refs lists the ${name} references in s.
func refs(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		j := strings.IndexByte(s[i:], '$')
		if j < 0 {
			break
		}
		i += j
		if i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				break
			}
			out = append(out, s[i+2:i+2+end])
			i += 2 + end + 1
		} else {
			i++
		}
	}
	return out
}

// pathValue walks dot-separated keys over a decoded JSON value; a numeric
// segment indexes an array. Missing: ok false.
func pathValue(raw json.RawMessage, path string) (v any, ok bool) {
	var cur any
	if err := json.Unmarshal(raw, &cur); err != nil {
		return nil, false
	}
	for _, seg := range strings.Split(path, ".") {
		switch t := cur.(type) {
		case map[string]any:
			c, found := t[seg]
			if !found {
				return nil, false
			}
			cur = c
		case []any:
			n, err := strconv.Atoi(seg)
			if err != nil || n < 0 || n >= len(t) {
				return nil, false
			}
			cur = t[n]
		default:
			return nil, false
		}
	}
	return cur, true
}

// render formats a result value for save/capture: string as is, number with
// FormatFloat 'f' -1, bool true/false, else compact JSON.
func render(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(raw)
	}
}
