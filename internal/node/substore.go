package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mikan/internal/fsutil"
	"mikan/internal/nodeapi"
)

// The subscription store: the profiles the panel rendered, kept on the node's disk so the
// node answers a subscriber's link while the panel is unreachable. A record is written whole
// and atomically, kept under a file named after the token's hash (never the token itself),
// and dropped when the panel stops sending it or its expiry passes.
//
// The path is a bearer token: it appears in no log line here, only its hash.

// SubsDir is the directory under the node's data directory that keeps the records.
const SubsDir = "subs"

var (
	// ErrSubTooBig: the record is past nodeapi.MaxSubRecordBytes.
	ErrSubTooBig = errors.New("sub_record_too_big")
	// ErrSubFull: the node keeps as many records, or as many bytes, as it may.
	ErrSubFull = errors.New("subs_full")
	// ErrSubBadPath: the record is not one this node would serve (a path that is no token,
	// no bodies, a body without its format).
	ErrSubBadPath = errors.New("sub_record_bad_path")
)

type subStore struct {
	dir string
	log *slog.Logger
	now func() time.Time

	mu     sync.RWMutex
	index  map[string]*subMeta // path → the record's file and facts
	bytes  int64
	gen    int64     // the newest generation seen
	synced time.Time // when the last prune (a completed full sync) ran
}

// subMeta is what the store keeps about a record without reading its file.
type subMeta struct {
	file    string
	expires time.Time
	gen     int64
	bytes   int64
}

// subFile is one record on the disk: the panel's record plus where it came from.
type subFile struct {
	nodeapi.SubRecord
	Gen     int64     `json:"generation"`
	SavedAt time.Time `json:"saved_at"`
}

// openSubs loads the store from dir, keeping whatever a previous run left: a restart of the
// node keeps answering. A file that does not read is removed — it cannot be served anyway.
func openSubs(dir string, now func() time.Time, log *slog.Logger) *subStore {
	s := &subStore{dir: dir, now: now, log: log, index: map[string]*subMeta{}}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Warn("subscriptions: the store's directory", "err", err)
		return s
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Warn("subscriptions: the store's directory", "err", err)
		return s
	}
	var broken int
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			broken++
			continue
		}
		var f subFile
		if err := json.Unmarshal(raw, &f); err != nil || !validSubPath(f.Path) || subFileOf(f.Path) != e.Name() {
			broken++
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		s.index[f.Path] = &subMeta{file: e.Name(), expires: f.Expires, gen: f.Gen, bytes: info.Size()}
		s.bytes += info.Size()
		if f.Gen > s.gen {
			s.gen = f.Gen
		}
	}
	if broken > 0 {
		log.Warn("subscriptions: damaged records left out", "n", broken)
	}
	if len(s.index) > 0 {
		log.Info("subscriptions: the store is loaded", "records", len(s.index), "bytes", s.bytes)
	}
	if len(s.index) > nodeapi.MaxSubRecords {
		log.Warn("subscriptions: more records than a node keeps", "n", len(s.index), "max", nodeapi.MaxSubRecords)
	}
	return s
}

// subFileOf is the file a path's record lives under: the token's hash, never the token.
func subFileOf(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:]) + ".json"
}

// validSubPath: the tokens the panel hands out, and nothing that could walk the directory.
func validSubPath(p string) bool {
	if p == "" || len(p) > nodeapi.MaxSubPath {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// put stores one record of a sync. A record that is too big or would overflow the store is
// refused whole, with the reason; the panel logs the reason, never the path.
func (s *subStore) put(gen int64, rec nodeapi.SubRecord) error {
	if !validSubPath(rec.Path) {
		return ErrSubBadPath
	}
	if len(rec.Bodies) == 0 || len(rec.Bodies) > nodeapi.MaxSubBodies {
		return ErrSubBadPath
	}
	f := subFile{SubRecord: rec, Gen: gen, SavedAt: s.now()}
	f.Bodies = make([]nodeapi.SubBody, len(rec.Bodies))
	var size int
	for i, b := range rec.Bodies {
		if b.Format == "" || len(b.Body) == 0 {
			return ErrSubBadPath
		}
		size += len(b.Body)
		f.Bodies[i] = b
	}
	if size > nodeapi.MaxSubRecordBytes {
		return ErrSubTooBig
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.index[rec.Path]
	var oldBytes int64
	if old != nil {
		oldBytes = old.bytes
	}
	if old == nil && len(s.index) >= nodeapi.MaxSubRecords {
		s.mu.Unlock()
		return ErrSubFull
	}
	if s.bytes-oldBytes+int64(len(raw)) > nodeapi.MaxSubsBytes {
		s.mu.Unlock()
		return ErrSubFull
	}
	s.mu.Unlock()
	if err := fsutil.WriteFileAtomic(filepath.Join(s.dir, subFileOf(rec.Path)), raw, 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.bytes += int64(len(raw)) - oldBytes
	s.index[rec.Path] = &subMeta{file: subFileOf(rec.Path), expires: f.Expires, gen: gen, bytes: int64(len(raw))}
	if gen > s.gen {
		s.gen = gen
	}
	s.mu.Unlock()
	return nil
}

// remove drops the records of paths at once and reports how many went. The paths are tokens
// and stay out of the log.
func (s *subStore) remove(paths []string) int {
	var gone []*subMeta
	s.mu.Lock()
	for _, p := range paths {
		if m, ok := s.index[p]; ok {
			gone = append(gone, m)
			s.bytes -= m.bytes
			delete(s.index, p)
		}
	}
	s.mu.Unlock()
	for _, m := range gone {
		_ = os.Remove(filepath.Join(s.dir, m.file))
	}
	return len(gone)
}

// prune ends a full sync: every record not sent with this generation is dropped.
func (s *subStore) prune(gen int64) int {
	var gone []*subMeta
	s.mu.Lock()
	for p, m := range s.index {
		if m.gen < gen {
			gone = append(gone, m)
			s.bytes -= m.bytes
			delete(s.index, p)
		}
	}
	if gen > s.gen {
		s.gen = gen
	}
	s.synced = s.now()
	s.mu.Unlock()
	for _, m := range gone {
		_ = os.Remove(filepath.Join(s.dir, m.file))
	}
	return len(gone)
}

// subLookup is what a request for a path gets: the record, or expired when the store had it
// but its time is over.
type subLookup struct {
	rec     subFile
	expired bool
}

// lookup finds a record by its path. ok is false for a path the node was never given: the
// node answers 404 then, saying nothing about whether the token ever existed.
func (s *subStore) lookup(path string) (subLookup, bool) {
	s.mu.RLock()
	m, ok := s.index[path]
	s.mu.RUnlock()
	if !ok {
		return subLookup{}, false
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, m.file))
	if err != nil {
		return subLookup{expired: true}, true
	}
	var f subFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return subLookup{expired: true}, true
	}
	return subLookup{rec: f, expired: !f.Expires.After(s.now())}, true
}

// status is the store as the admin sees it: counts and bytes, never a path.
func (s *subStore) status() nodeapi.SubsStatus {
	now := s.now()
	st := nodeapi.SubsStatus{}
	s.mu.RLock()
	defer s.mu.RUnlock()
	st.Records, st.Bytes, st.Generation, st.SyncedAt = len(s.index), s.bytes, s.gen, s.synced
	for _, m := range s.index {
		if !m.expires.After(now) {
			st.Expired++
		}
	}
	return st
}

// paths is the store's paths, sorted: what a test counts, never a log line.
func (s *subStore) paths() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.index))
	for p := range s.index {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// String keeps a record's path out of a log line by accident: four characters and its facts.
func (f subFile) String() string {
	return fmt.Sprintf("subscription %s… (%d bodies, until %s)", f.Path[:min(4, len(f.Path))], len(f.Bodies), f.Expires.Format(time.RFC3339))
}
