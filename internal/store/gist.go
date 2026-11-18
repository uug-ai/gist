// Package store validates gists and keeps them, with every revision, as JSON
// files on disk (or in memory for tests and the self-test).
package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// File is one file of a gist.
type File struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// RevInfo summarises one revision in the gist's metadata.
type RevInfo struct {
	Number    int       `json:"number"`
	CreatedAt time.Time `json:"created_at"`
	Bytes     int64     `json:"bytes"`
}

// Meta is everything about a gist except the file contents.
type Meta struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	ForkOf      string     `json:"fork_of,omitempty"`
	// Public gists are listed on Discover and may be indexed by search
	// engines. Secret gists (the default) are only reachable by their link.
	Public    bool      `json:"public,omitempty"`
	TokenHash string    `json:"token_hash"`
	Revisions []RevInfo `json:"revisions"`
	// Bytes is the size of all revisions together, counted against the quota.
	Bytes int64 `json:"bytes"`
}

// Latest is the number of the newest revision.
func (m *Meta) Latest() int {
	if len(m.Revisions) == 0 {
		return 0
	}
	return m.Revisions[len(m.Revisions)-1].Number
}

// Expired reports whether the gist's expiry time has passed.
func (m *Meta) Expired(now time.Time) bool {
	return m.ExpiresAt != nil && !now.Before(*m.ExpiresAt)
}

// Revision is the content of a gist at one point in time.
type Revision struct {
	Number      int       `json:"number"`
	CreatedAt   time.Time `json:"created_at"`
	Description string    `json:"description"`
	Files       []File    `json:"files"`
}

// Limits bound what a single revision may contain.
type Limits struct {
	MaxFiles       int
	MaxBytes       int
	MaxDescription int
	MaxFilename    int
}

// DefaultLimits are used when the configuration doesn't override them.
var DefaultLimits = Limits{MaxFiles: 20, MaxBytes: 1 << 20, MaxDescription: 1000, MaxFilename: 100}

// ValidationError is a problem with what the user submitted. Its message is
// safe to show to them.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// IsValidation reports whether err is a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// Normalize cleans up a submitted description and files and checks them
// against the limits. Rows with neither a name nor content are dropped (an
// empty row of the web form), unnamed files are called gistfile1.txt, …,
// line endings become \n and the files are sorted by name, as on GitHub.
func Normalize(description string, files []File, l Limits) (string, []File, error) {
	description = strings.Join(strings.Fields(description), " ")
	if utf8.RuneCountInString(description) > l.MaxDescription {
		return "", nil, invalid("The description is longer than %d characters.", l.MaxDescription)
	}
	if !utf8.ValidString(description) {
		return "", nil, invalid("The description is not valid UTF-8.")
	}

	var out []File
	seen := map[string]bool{}
	total, unnamed := 0, 0
	for _, f := range files {
		name := strings.TrimSpace(f.Name)
		content := strings.ReplaceAll(f.Content, "\r\n", "\n")
		if name == "" && strings.TrimSpace(content) == "" {
			continue
		}
		if name == "" {
			for {
				unnamed++
				name = fmt.Sprintf("gistfile%d.txt", unnamed)
				if !seen[name] && !named(files, name) {
					break
				}
			}
		}
		if err := checkName(name, l.MaxFilename); err != nil {
			return "", nil, err
		}
		if seen[name] {
			return "", nil, invalid("There are two files called %q.", name)
		}
		seen[name] = true
		if strings.TrimSpace(content) == "" {
			return "", nil, invalid("The file %q is empty.", name)
		}
		if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			return "", nil, invalid("The file %q is not text (only UTF-8 text is supported).", name)
		}
		total += len(content)
		out = append(out, File{Name: name, Content: content})
	}
	if len(out) == 0 {
		return "", nil, invalid("A gist needs at least one file with content.")
	}
	if len(out) > l.MaxFiles {
		return "", nil, invalid("A gist can have at most %d files.", l.MaxFiles)
	}
	if total > l.MaxBytes {
		return "", nil, invalid("The files are larger than %s together.", HumanBytes(int64(l.MaxBytes)))
	}
	SortFiles(out)
	return description, out, nil
}

func named(files []File, name string) bool {
	for _, f := range files {
		if strings.TrimSpace(f.Name) == name {
			return true
		}
	}
	return false
}

func checkName(name string, max int) error {
	if !utf8.ValidString(name) {
		return invalid("A filename is not valid UTF-8.")
	}
	if utf8.RuneCountInString(name) > max {
		return invalid("The filename %q is longer than %d characters.", name, max)
	}
	if name == "." || name == ".." {
		return invalid("%q is not a valid filename.", name)
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return invalid("The filename %q can't contain slashes or control characters.", name)
		}
	}
	return nil
}

// SortFiles orders files by name, ignoring case first.
func SortFiles(files []File) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := strings.ToLower(files[i].Name), strings.ToLower(files[j].Name)
		if a != b {
			return a < b
		}
		return files[i].Name < files[j].Name
	})
}

// Size is the number of bytes of content in files.
func Size(files []File) int64 {
	var n int64
	for _, f := range files {
		n += int64(len(f.Content))
	}
	return n
}

// HumanBytes formats a size like "1.5 KiB".
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
