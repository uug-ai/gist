package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	desc, files, err := Normalize("  a\n description ", []File{
		{Name: "", Content: ""},
		{Name: "b.go", Content: "package b\r\n"},
		{Name: "", Content: "notes"},
		{Name: "A.txt", Content: "x"},
	}, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if desc != "a description" {
		t.Errorf("description %q", desc)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, ","); got != "A.txt,b.go,gistfile1.txt" {
		t.Errorf("files %s", got)
	}
	if files[1].Content != "package b\n" {
		t.Errorf("CRLF not normalised: %q", files[1].Content)
	}
}

func TestNormalizeRejects(t *testing.T) {
	cases := map[string][]File{
		"no files":  {{Name: "", Content: " "}},
		"empty":     {{Name: "a.txt", Content: "  "}},
		"slash":     {{Name: "a/b", Content: "x"}},
		"dotdot":    {{Name: "..", Content: "x"}},
		"duplicate": {{Name: "a", Content: "x"}, {Name: "a", Content: "y"}},
		"binary":    {{Name: "a", Content: "x\x00y"}},
		"too big":   {{Name: "a", Content: strings.Repeat("x", DefaultLimits.MaxBytes+1)}},
	}
	for name, files := range cases {
		if _, _, err := Normalize("", files, DefaultLimits); !IsValidation(err) {
			t.Errorf("%s: got %v, want a validation error", name, err)
		}
	}
	many := make([]File, DefaultLimits.MaxFiles+1)
	for i := range many {
		many[i] = File{Name: strings.Repeat("f", i+1), Content: "x"}
	}
	if _, _, err := Normalize("", many, DefaultLimits); !IsValidation(err) {
		t.Errorf("too many files: %v", err)
	}
}

func TestLifecycle(t *testing.T) {
	for name, open := range map[string]func(t *testing.T, o Options) *Store{
		"memory": func(t *testing.T, o Options) *Store { return NewMemory(o) },
		"files": func(t *testing.T, o Options) *Store {
			s, err := Open(t.TempDir(), o)
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t, Options{})
			m, token, err := s.Create(CreateInput{Description: "d", Files: []File{{Name: "a.go", Content: "package a\n"}}})
			if err != nil {
				t.Fatal(err)
			}
			if !ValidID(m.ID) || !Authorized(m, token) || Authorized(m, token+"x") || Authorized(m, "") {
				t.Fatalf("id %q or key check wrong", m.ID)
			}
			if s.Usage() != 10 {
				t.Errorf("usage %d", s.Usage())
			}
			// Same content: no new revision.
			if m, err = s.Update(m.ID, "d", []File{{Name: "a.go", Content: "package a\n"}}); err != nil || m.Latest() != 1 {
				t.Fatalf("no-op update: %v, latest %d", err, m.Latest())
			}
			if m, err = s.Update(m.ID, "d2", []File{{Name: "a.go", Content: "package b\n"}}); err != nil || m.Latest() != 2 {
				t.Fatalf("update: %v", err)
			}
			got, err := s.Get(m.ID)
			if err != nil || got.Description != "d2" || len(got.Revisions) != 2 {
				t.Fatalf("get: %+v %v", got, err)
			}
			r1, err := s.Revision(m.ID, 1)
			if err != nil || r1.Files[0].Content != "package a\n" {
				t.Fatalf("revision 1: %+v %v", r1, err)
			}
			if _, err := s.Revision(m.ID, 3); !errors.Is(err, ErrNotFound) {
				t.Fatalf("revision 3: %v", err)
			}
			if err := s.Delete(m.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(m.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("after delete: %v", err)
			}
			if s.Usage() != 0 {
				t.Errorf("usage after delete %d", s.Usage())
			}
		})
	}
}

func TestReopenCountsUsage(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(CreateInput{Files: []File{{Name: "a", Content: "12345"}}}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Usage() != 5 {
		t.Fatalf("usage after reopen %d", s2.Usage())
	}
}

func TestExpiryAndSweep(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := NewMemory(Options{Now: func() time.Time { return now }})
	m, _, err := s.Create(CreateInput{Files: []File{{Name: "a", Content: "x"}}, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	keep, _, _ := s.Create(CreateInput{Files: []File{{Name: "a", Content: "x"}}})
	if _, err := s.Get(m.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := s.Get(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired gist still readable: %v", err)
	}
	if n, err := s.Sweep(); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if _, err := s.Get(keep.ID); err != nil {
		t.Fatalf("gist without expiry swept: %v", err)
	}
}

func TestQuotaAndRevisionLimit(t *testing.T) {
	s := NewMemory(Options{Quota: 10, MaxRevisions: 2})
	m, _, err := s.Create(CreateInput{Files: []File{{Name: "a", Content: "12345"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(CreateInput{Files: []File{{Name: "a", Content: "123456"}}}); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota: %v", err)
	}
	if _, err := s.Update(m.ID, "", []File{{Name: "a", Content: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(m.ID, "", []File{{Name: "a", Content: "2"}}); !errors.Is(err, ErrTooManyRevisions) {
		t.Fatalf("revision limit: %v", err)
	}
}

func TestInvalidIDs(t *testing.T) {
	s := NewMemory(Options{})
	for _, id := range []string{"", "../etc", "ABCDEF0123456789ABCDEF0123456789", strings.Repeat("a", 33)} {
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q): %v", id, err)
		}
	}
}
