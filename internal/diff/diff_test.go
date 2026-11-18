package diff

import (
	"math/rand"
	"strings"
	"testing"
)

// apply rebuilds both sides from a diff.
func apply(lines []Line) (old, new []string) {
	for _, l := range lines {
		if l.Op != Insert {
			old = append(old, l.Text)
		}
		if l.Op != Delete {
			new = append(new, l.Text)
		}
	}
	return old, new
}

func TestLinesRebuildsBothSides(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	words := []string{"a", "b", "c", "d", "e"}
	gen := func() []string {
		out := make([]string, r.Intn(30))
		for i := range out {
			out[i] = words[r.Intn(len(words))]
		}
		return out
	}
	for i := 0; i < 2000; i++ {
		a, b := gen(), gen()
		lines := Lines(a, b)
		gotA, gotB := apply(lines)
		if strings.Join(gotA, ",") != strings.Join(a, ",") || strings.Join(gotB, ",") != strings.Join(b, ",") {
			t.Fatalf("diff of %v and %v rebuilds %v and %v", a, b, gotA, gotB)
		}
		oldNo, newNo := 0, 0
		for _, l := range lines {
			if l.Op != Insert {
				oldNo++
				if l.OldNo != oldNo {
					t.Fatalf("old line number %d, want %d", l.OldNo, oldNo)
				}
			}
			if l.Op != Delete {
				newNo++
				if l.NewNo != newNo {
					t.Fatalf("new line number %d, want %d", l.NewNo, newNo)
				}
			}
		}
	}
}

func TestLinesIsMinimal(t *testing.T) {
	a := SplitLines("one\ntwo\nthree\nfour\n")
	b := SplitLines("one\n2\nthree\nfour\nfive\n")
	added, removed := Stat(Lines(a, b))
	if added != 2 || removed != 1 {
		t.Fatalf("got +%d -%d, want +2 -1", added, removed)
	}
}

func TestHunks(t *testing.T) {
	var a, b []string
	for i := 0; i < 40; i++ {
		a = append(a, string(rune('A'+i%26)))
	}
	b = append(b, a...)
	b[5] = "changed"
	b[30] = "changed too"
	hunks := Hunks(Lines(a, b), 3)
	if len(hunks) != 2 {
		t.Fatalf("got %d hunks, want 2", len(hunks))
	}
	h := hunks[0]
	if h.OldStart != 3 || h.OldLines != 7 || h.NewStart != 3 || h.NewLines != 7 {
		t.Fatalf("first hunk %+v", h)
	}
	// Close changes merge into one hunk.
	b[10] = "close"
	if got := len(Hunks(Lines(a, b), 3)); got != 2 {
		t.Fatalf("got %d hunks, want 2 after a change 5 lines later", got)
	}
}

func TestLargeDiffFallsBack(t *testing.T) {
	var a, b []string
	for i := 0; i < 3000; i++ {
		a = append(a, "x"+string(rune(i)))
		b = append(b, "y"+string(rune(i)))
	}
	added, removed := Stat(Lines(a, b))
	if added != 3000 || removed != 3000 {
		t.Fatalf("got +%d -%d", added, removed)
	}
}
