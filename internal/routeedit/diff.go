package routeedit

import (
	"fmt"
	"strings"
)

const diffContext = 3

// maxDiffCells bounds the table of the line diff; a larger change is written as one replaced block.
const maxDiffCells = 4_000_000

// UnifiedDiff is a unified text diff of two texts, lines compared whole, for the record of an edit (readable on the page and pasteable into a
// PR). "" when they are equal.
func UnifiedDiff(aName, bName, a, b string) string {
	if a == b {
		return ""
	}
	al, bl := splitLines(a), splitLines(b)
	pre := 0
	for pre < len(al) && pre < len(bl) && al[pre] == bl[pre] {
		pre++
	}
	suf := 0
	for suf < len(al)-pre && suf < len(bl)-pre && al[len(al)-1-suf] == bl[len(bl)-1-suf] {
		suf++
	}
	am, bm := al[pre:len(al)-suf], bl[pre:len(bl)-suf]
	type edit struct {
		op   byte // ' ', '-', '+'
		text string
	}
	var script []edit
	for _, l := range al[:pre] {
		script = append(script, edit{' ', l})
	}
	if len(am)*len(bm) > maxDiffCells {
		for _, l := range am {
			script = append(script, edit{'-', l})
		}
		for _, l := range bm {
			script = append(script, edit{'+', l})
		}
	} else {
		// longest common subsequence of the middle
		n, m := len(am), len(bm)
		lcs := make([][]int32, n+1)
		for i := range lcs {
			lcs[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if am[i] == bm[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else {
					lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && am[i] == bm[j]:
				script = append(script, edit{' ', am[i]})
				i++
				j++
			case j >= m || (i < n && lcs[i+1][j] >= lcs[i][j+1]):
				script = append(script, edit{'-', am[i]})
				i++
			default:
				script = append(script, edit{'+', bm[j]})
				j++
			}
		}
	}
	for _, l := range al[len(al)-suf:] {
		script = append(script, edit{' ', l})
	}
	// hunks: the changed lines with diffContext lines either side
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", aName, bName)
	ai, bi := 1, 1 // line numbers in a and b of script[k]
	starts := make([][2]int, len(script))
	for k, e := range script {
		starts[k] = [2]int{ai, bi}
		if e.op != '+' {
			ai++
		}
		if e.op != '-' {
			bi++
		}
	}
	var changed []int
	for k, e := range script {
		if e.op != ' ' {
			changed = append(changed, k)
		}
	}
	for g := 0; g < len(changed); {
		first, last := changed[g], changed[g]
		g++
		for g < len(changed) && changed[g]-last <= 2*diffContext+1 { // the unchanged lines between two changes fit in both hunks' context
			last = changed[g]
			g++
		}
		lo, end := max(0, first-diffContext), min(len(script), last+1+diffContext)
		var aN, bN int
		for _, e := range script[lo:end] {
			if e.op != '+' {
				aN++
			}
			if e.op != '-' {
				bN++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", starts[lo][0], aN, starts[lo][1], bN)
		for _, e := range script[lo:end] {
			out.WriteByte(e.op)
			out.WriteString(e.text)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
