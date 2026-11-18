package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sync"
	"time"
)

var (
	// ErrNotFound means the gist (or revision) doesn't exist or has expired.
	ErrNotFound = errors.New("gist not found")
	// ErrQuota means storing the gist would exceed the storage quota.
	ErrQuota = errors.New("storage quota reached")
	// ErrTooManyRevisions means the gist can't be edited any further.
	ErrTooManyRevisions = errors.New("too many revisions")
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidID reports whether id looks like a gist id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Store keeps gists. It is safe for concurrent use; writes are serialised.
type Store struct {
	mu           sync.RWMutex
	b            backend
	usage        int64
	quota        int64
	maxRevisions int
	now          func() time.Time
}

// Options configure a Store.
type Options struct {
	// Quota is the most bytes of content the store keeps, all revisions of
	// all gists together. Zero means no limit.
	Quota int64
	// MaxRevisions is how many revisions one gist may have. Zero means 100.
	MaxRevisions int
	// Now replaces time.Now in tests.
	Now func() time.Time
}

// Open opens (and creates) a store in dir.
func Open(dir string, o Options) (*Store, error) {
	b, err := newFileBackend(dir)
	if err != nil {
		return nil, err
	}
	return newStore(b, o)
}

// NewMemory returns a store that keeps everything in memory.
func NewMemory(o Options) *Store {
	s, _ := newStore(newMemBackend(), o)
	return s
}

func newStore(b backend, o Options) (*Store, error) {
	s := &Store{b: b, quota: o.Quota, maxRevisions: o.MaxRevisions, now: o.Now}
	if s.maxRevisions == 0 {
		s.maxRevisions = 100
	}
	if s.now == nil {
		s.now = time.Now
	}
	ids, err := b.IDs()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		m, err := s.readMeta(id)
		if err != nil {
			continue // half-written gist; Sweep doesn't touch it either
		}
		s.usage += m.Bytes
	}
	return s, nil
}

// Usage is the number of content bytes stored.
func (s *Store) Usage() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usage
}

// CreateInput describes a new gist. Description and Files must already have
// gone through Normalize.
type CreateInput struct {
	Description string
	Files       []File
	ExpiresIn   time.Duration
	ForkOf      string
}

// Create stores a new gist and returns it with its edit key.
func (s *Store) Create(in CreateInput) (*Meta, string, error) {
	size := Size(in.Files)
	token, hash, err := newToken()
	if err != nil {
		return nil, "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quota > 0 && s.usage+size > s.quota {
		return nil, "", ErrQuota
	}
	var id string
	for {
		if id, err = randomHex(16); err != nil {
			return nil, "", err
		}
		if _, err := s.b.Read(id, "meta.json"); errors.Is(err, fs.ErrNotExist) {
			break
		}
	}
	now := s.now().UTC()
	m := &Meta{
		ID:          id,
		Description: in.Description,
		CreatedAt:   now,
		UpdatedAt:   now,
		ForkOf:      in.ForkOf,
		TokenHash:   hash,
		Revisions:   []RevInfo{{Number: 1, CreatedAt: now, Bytes: size}},
		Bytes:       size,
	}
	if in.ExpiresIn > 0 {
		t := now.Add(in.ExpiresIn)
		m.ExpiresAt = &t
	}
	rev := &Revision{Number: 1, CreatedAt: now, Description: in.Description, Files: in.Files}
	if err := s.writeJSON(id, revName(1), rev); err != nil {
		_ = s.b.Remove(id)
		return nil, "", err
	}
	if err := s.writeJSON(id, "meta.json", m); err != nil {
		_ = s.b.Remove(id)
		return nil, "", err
	}
	s.usage += size
	return m, token, nil
}

// Get returns a gist's metadata.
func (s *Store) Get(id string) (*Meta, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, err := s.readMeta(id)
	if err != nil {
		return nil, err
	}
	if m.Expired(s.now()) {
		return nil, ErrNotFound
	}
	return m, nil
}

// Revision returns revision n of a gist.
func (s *Store) Revision(id string, n int) (*Revision, error) {
	if !ValidID(id) || n < 1 {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, err := s.readMeta(id)
	if err != nil {
		return nil, err
	}
	if m.Expired(s.now()) {
		return nil, ErrNotFound
	}
	data, err := s.b.Read(id, revName(n))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var r Revision
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("revision %d of %s: %w", n, id, err)
	}
	return &r, nil
}

// Update adds a revision with new content. Nothing changes when the content
// equals the latest revision.
func (s *Store) Update(id, description string, files []File) (*Meta, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(id)
	if err != nil {
		return nil, err
	}
	if m.Expired(s.now()) {
		return nil, ErrNotFound
	}
	latest := m.Latest()
	data, err := s.b.Read(id, revName(latest))
	if err != nil {
		return nil, err
	}
	var prev Revision
	if err := json.Unmarshal(data, &prev); err != nil {
		return nil, err
	}
	if prev.Description == description && sameFiles(prev.Files, files) {
		return m, nil
	}
	if len(m.Revisions) >= s.maxRevisions {
		return nil, ErrTooManyRevisions
	}
	size := Size(files)
	if s.quota > 0 && s.usage+size > s.quota {
		return nil, ErrQuota
	}
	now := s.now().UTC()
	n := latest + 1
	rev := &Revision{Number: n, CreatedAt: now, Description: description, Files: files}
	if err := s.writeJSON(id, revName(n), rev); err != nil {
		return nil, err
	}
	m.Description = description
	m.UpdatedAt = now
	m.Revisions = append(m.Revisions, RevInfo{Number: n, CreatedAt: now, Bytes: size})
	m.Bytes += size
	if err := s.writeJSON(id, "meta.json", m); err != nil {
		return nil, err
	}
	s.usage += size
	return m, nil
}

// Delete removes a gist and all its revisions.
func (s *Store) Delete(id string) error {
	if !ValidID(id) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(id)
	if err != nil {
		return err
	}
	if err := s.b.Remove(id); err != nil {
		return err
	}
	s.usage -= m.Bytes
	return nil
}

// Sweep deletes expired gists and returns how many it deleted.
func (s *Store) Sweep() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, err := s.b.IDs()
	if err != nil {
		return 0, err
	}
	now, n := s.now(), 0
	for _, id := range ids {
		m, err := s.readMeta(id)
		if err != nil || !m.Expired(now) {
			continue
		}
		if err := s.b.Remove(id); err != nil {
			return n, err
		}
		s.usage -= m.Bytes
		n++
	}
	return n, nil
}

// Authorized reports whether token is the gist's edit key.
func Authorized(m *Meta, token string) bool {
	if m == nil || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(m.TokenHash)) == 1
}

func (s *Store) readMeta(id string) (*Meta, error) {
	data, err := s.b.Read(id, "meta.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("metadata of %s: %w", id, err)
	}
	return &m, nil
}

func (s *Store) writeJSON(id, name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.b.Write(id, name, data)
}

func revName(n int) string { return fmt.Sprintf("r%d.json", n) }

func sameFiles(a, b []File) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
