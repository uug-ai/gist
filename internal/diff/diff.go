// Package diff computes line diffs (Myers' algorithm) and groups them into
// unified-diff hunks.
package diff

import "strings"

// Op is what happened to a line.
type Op int

const (
	Equal Op = iota
	Insert
	Delete
)

// Line is one line of a diff. OldNo and NewNo are 1-based line numbers, 0
// when the line doesn't exist on that side.
type Line struct {
	Op    Op
	Text  string
	OldNo int
	NewNo int
}

// maxEdits bounds the work for very different inputs; beyond it the whole
// middle section is shown as replaced.
const maxEdits = 1000

// SplitLines splits text into lines without their line endings.
func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// Lines diffs a against b.
func Lines(a, b []string) []Line {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	out := make([]Line, 0, len(a)+len(b))
	for i := 0; i < pre; i++ {
		out = append(out, Line{Op: Equal, Text: a[i], OldNo: i + 1, NewNo: i + 1})
	}
	for _, l := range myers(a[pre:len(a)-suf], b[pre:len(b)-suf]) {
		if l.OldNo > 0 {
			l.OldNo += pre
		}
		if l.NewNo > 0 {
			l.NewNo += pre
		}
		out = append(out, l)
	}
	for i := 0; i < suf; i++ {
		oi, ni := len(a)-suf+i, len(b)-suf+i
		out = append(out, Line{Op: Equal, Text: a[oi], OldNo: oi + 1, NewNo: ni + 1})
	}
	return out
}

func myers(a, b []string) []Line {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	max := n + m
	offset := max + 1
	v := make([]int, 2*max+3)
	// trace[d] holds v for k in [-d-1, d+1] as it was before round d.
	var trace [][]int
	found := false
	for d := 0; d <= max && !found; d++ {
		if d > maxEdits {
			return replaceAll(a, b)
		}
		trace = append(trace, append([]int(nil), v[offset-d-1:offset+d+2]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}

	var rev []Line
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		snap := trace[d]
		at := func(k int) int { return snap[k+d+1] }
		k := x - y
		var prevK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, Line{Op: Equal, Text: a[x-1], OldNo: x, NewNo: y})
			x--
			y--
		}
		if d > 0 {
			if x == prevX {
				rev = append(rev, Line{Op: Insert, Text: b[y-1], NewNo: y})
			} else {
				rev = append(rev, Line{Op: Delete, Text: a[x-1], OldNo: x})
			}
		}
		x, y = prevX, prevY
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

func replaceAll(a, b []string) []Line {
	out := make([]Line, 0, len(a)+len(b))
	for i, s := range a {
		out = append(out, Line{Op: Delete, Text: s, OldNo: i + 1})
	}
	for i, s := range b {
		out = append(out, Line{Op: Insert, Text: s, NewNo: i + 1})
	}
	return out
}

// Hunk is a group of changed lines with surrounding context.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Lines              []Line
}

// Hunks groups a diff into hunks with context unchanged lines around each
// change, merging changes that are close together.
func Hunks(lines []Line, context int) []Hunk {
	var hunks []Hunk
	n := len(lines)
	for i := 0; i < n; {
		for i < n && lines[i].Op == Equal {
			i++
		}
		if i >= n {
			break
		}
		start := i - context
		if start < 0 {
			start = 0
		}
		end := i
		for {
			for end < n && lines[end].Op != Equal {
				end++
			}
			j := end
			for j < n && lines[j].Op == Equal {
				j++
			}
			if j < n && j-end <= 2*context {
				end = j
				continue
			}
			end += context
			if end > n {
				end = n
			}
			break
		}
		hunks = append(hunks, makeHunk(lines, start, end))
		i = end
	}
	return hunks
}

func makeHunk(lines []Line, start, end int) Hunk {
	h := Hunk{Lines: lines[start:end]}
	// Line numbers before the hunk: the last numbered line on each side.
	oldBefore, newBefore := 0, 0
	for i := start - 1; i >= 0 && (oldBefore == 0 || newBefore == 0); i-- {
		if oldBefore == 0 && lines[i].OldNo > 0 {
			oldBefore = lines[i].OldNo
		}
		if newBefore == 0 && lines[i].NewNo > 0 {
			newBefore = lines[i].NewNo
		}
	}
	for _, l := range h.Lines {
		if l.Op != Insert {
			h.OldLines++
		}
		if l.Op != Delete {
			h.NewLines++
		}
	}
	h.OldStart, h.NewStart = oldBefore+1, newBefore+1
	if h.OldLines == 0 {
		h.OldStart = oldBefore
	}
	if h.NewLines == 0 {
		h.NewStart = newBefore
	}
	return h
}

// Stat counts inserted and deleted lines.
func Stat(lines []Line) (added, removed int) {
	for _, l := range lines {
		switch l.Op {
		case Insert:
			added++
		case Delete:
			removed++
		}
	}
	return added, removed
}
