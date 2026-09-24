package logs

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

// Store keeps container output beyond the container: one file per project, service and
// UTC day (<dir>/<project id>/<service>/<YYYY-MM-DD>.jsonl), gzipped once the day is
// over. Keys are the project and the service, not the container, so the history
// survives a container that is recreated.
type Store struct {
	dir string

	mu   sync.Mutex
	last map[Key]time.Time // newest stored line per key, loaded lazily
}

// Key names one service's history.
type Key struct {
	Project string
	Service string
}

var (
	projectDirRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	serviceRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}(:[0-9a-f-]{36})?$`)
	dayFileRe    = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl(\.gz)?$`)
)

const dayLayout = "2006-01-02"

// ErrBadKey is returned for keys that cannot name a directory.
var ErrBadKey = errors.New("invalid log history key")

// OpenStore uses dir (created if missing) for the history.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Store{dir: dir, last: map[Key]time.Time{}}, nil
}

// Dir is the directory the history lives in.
func (s *Store) Dir() string { return s.dir }

func (s *Store) keyDir(k Key) (string, error) {
	if !projectDirRe.MatchString(k.Project) || !serviceRe.MatchString(k.Service) {
		return "", fmt.Errorf("%w: %s/%s", ErrBadKey, k.Project, k.Service)
	}
	return filepath.Join(s.dir, k.Project, strings.ReplaceAll(k.Service, ":", "-")), nil
}

// record is the on-disk form of a line; short names keep the files small.
type record struct {
	T time.Time `json:"t"`
	S string    `json:"s,omitempty"` // "e" = stderr, "" = stdout
	M string    `json:"m"`
}

func toRecord(l docker.LogLine) record {
	r := record{T: l.Time.UTC(), M: l.Text}
	if l.Stream == "stderr" {
		r.S = "e"
	}
	return r
}

func (r record) line() docker.LogLine {
	l := docker.LogLine{Time: r.T, Stream: "stdout", Text: r.M}
	if r.S == "e" {
		l.Stream = "stderr"
	}
	return l
}

// Append stores lines (oldest first) under k. Lines without a time get the current one.
func (s *Store) Append(k Key, lines []docker.LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	dir, err := s.keyDir(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	var (
		buf     bytes.Buffer
		day     string
		newest  time.Time
		flushed error
	)
	flush := func() {
		if buf.Len() == 0 || flushed != nil {
			return
		}
		path := filepath.Join(dir, day+".jsonl")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
		if errors.Is(err, fs.ErrNotExist) {
			// Prune removed the directory the moment it was empty.
			if err = os.MkdirAll(dir, 0o750); err == nil {
				f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
			}
		}
		if err != nil {
			flushed = err
			return
		}
		if _, err := f.Write(buf.Bytes()); err != nil {
			flushed = err
		}
		if err := f.Close(); err != nil && flushed == nil {
			flushed = err
		}
		buf.Reset()
	}
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	now := time.Now()
	for _, l := range lines {
		if l.Time.IsZero() {
			l.Time = now
		}
		if d := l.Time.UTC().Format(dayLayout); d != day {
			flush()
			day = d
		}
		if err := enc.Encode(toRecord(l)); err != nil {
			return err
		}
		if l.Time.After(newest) {
			newest = l.Time
		}
	}
	flush()
	if flushed != nil {
		return flushed
	}
	s.mu.Lock()
	if newest.After(s.last[k]) {
		s.last[k] = newest
	}
	s.mu.Unlock()
	return nil
}

// dayFile is one day of one key, plain, gzipped or – briefly, or after a late write to
// a compressed day – both.
type dayFile struct {
	day   string
	plain string
	gz    string
}

func (s *Store) days(k Key) ([]dayFile, error) {
	dir, err := s.keyDir(k)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	byDay := map[string]*dayFile{}
	for _, e := range entries {
		m := dayFileRe.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		d := byDay[m[1]]
		if d == nil {
			d = &dayFile{day: m[1]}
			byDay[m[1]] = d
		}
		if m[2] != "" {
			d.gz = filepath.Join(dir, e.Name())
		} else {
			d.plain = filepath.Join(dir, e.Name())
		}
	}
	out := make([]dayFile, 0, len(byDay))
	for _, d := range byDay {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].day < out[j].day })
	return out, nil
}

// Has reports whether anything is stored under k.
func (s *Store) Has(k Key) bool {
	d, err := s.days(k)
	return err == nil && len(d) > 0
}

// Oldest returns the time of the first stored line under k (zero = none).
func (s *Store) Oldest(ctx context.Context, k Key) time.Time {
	days, err := s.days(k)
	if err != nil || len(days) == 0 {
		return time.Time{}
	}
	var first time.Time
	_ = readDay(ctx, days[0], func(l docker.LogLine) bool {
		first = l.Time
		return false
	})
	return first
}

// Last returns the time of the newest stored line under k (zero = none).
func (s *Store) Last(k Key) time.Time {
	s.mu.Lock()
	t, ok := s.last[k]
	s.mu.Unlock()
	if ok {
		return t
	}
	days, err := s.days(k)
	if err != nil {
		return time.Time{}
	}
	for i := len(days) - 1; i >= 0 && t.IsZero(); i-- {
		_ = readDay(context.Background(), days[i], func(l docker.LogLine) bool {
			if l.Time.After(t) {
				t = l.Time
			}
			return true
		})
	}
	s.mu.Lock()
	if cur, ok := s.last[k]; !ok || t.After(cur) {
		s.last[k] = t
	} else {
		t = cur
	}
	s.mu.Unlock()
	return t
}

// Scan emits the lines of k in [since, until] (zero = open), oldest first.
func (s *Store) Scan(ctx context.Context, k Key, since, until time.Time, emit func(docker.LogLine)) error {
	days, err := s.days(k)
	if err != nil {
		return err
	}
	for _, d := range days {
		if !inDays(d.day, since, until) {
			continue
		}
		err := readDay(ctx, d, func(l docker.LogLine) bool {
			if (since.IsZero() || !l.Time.Before(since)) && (until.IsZero() || !l.Time.After(until)) {
				emit(l)
			}
			return true
		})
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Tail returns the last n lines of k up to until (zero = now) and how many lines there
// are in total. It parses only the newest days it needs and merely counts the rest.
func (s *Store) Tail(ctx context.Context, k Key, until time.Time, n int) ([]docker.LogLine, int, error) {
	days, err := s.days(k)
	if err != nil {
		return nil, 0, err
	}
	var (
		out   []docker.LogLine
		total int
	)
	for i := len(days) - 1; i >= 0; i-- {
		d := days[i]
		if !inDays(d.day, time.Time{}, until) {
			continue
		}
		if len(out) >= n && (until.IsZero() || d.day < until.UTC().Format(dayLayout)) {
			c, err := countDay(d)
			if err != nil {
				return nil, 0, err
			}
			total += c
			continue
		}
		var lines []docker.LogLine
		err := readDay(ctx, d, func(l docker.LogLine) bool {
			if until.IsZero() || !l.Time.After(until) {
				lines = append(lines, l)
			}
			return true
		})
		if err != nil {
			return nil, 0, err
		}
		total += len(lines)
		out = append(lines, out...)
		if len(out) > n {
			out = out[len(out)-n:]
		}
	}
	return out, total, ctx.Err()
}

func inDays(day string, since, until time.Time) bool {
	return (since.IsZero() || day >= since.UTC().Format(dayLayout)) && (until.IsZero() || day <= until.UTC().Format(dayLayout))
}

// maxRecord bounds one stored line: Docker splits lines at 16 KB, Envoryx at 64 KB,
// plus the JSON around it.
const maxRecord = 1 << 20

// readDay reads the gzipped part of a day, then the plain one, until fn returns false.
// A line that does not parse (a write in progress) is skipped.
func readDay(ctx context.Context, d dayFile, fn func(docker.LogLine) bool) error {
	for _, path := range []string{d.gz, d.plain} {
		if path == "" {
			continue
		}
		more, err := readFile(ctx, path, fn)
		if err != nil || !more {
			return err
		}
	}
	return nil
}

func openDayFile(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{zr, f}, nil
}

func readFile(ctx context.Context, path string, fn func(docker.LogLine) bool) (bool, error) {
	r, err := openDayFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil // compressed or pruned meanwhile
	}
	if err != nil {
		return false, err
	}
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxRecord)
	for i := 0; sc.Scan(); i++ {
		if i%4096 == 0 && ctx.Err() != nil {
			return false, ctx.Err()
		}
		var rec record
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if !fn(rec.line()) {
			return false, nil
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return true, nil
}

func countDay(d dayFile) (int, error) {
	n := 0
	for _, path := range []string{d.gz, d.plain} {
		if path == "" {
			continue
		}
		r, err := openDayFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		buf := make([]byte, 256<<10)
		for {
			c, err := r.Read(buf)
			n += bytes.Count(buf[:c], []byte{'\n'})
			if err == io.EOF {
				break
			}
			if err != nil {
				r.Close()
				return 0, err
			}
		}
		r.Close()
	}
	return n, nil
}

// Usage describes what the history occupies.
type Usage struct {
	Bytes  int64     `json:"bytes"`
	Files  int       `json:"files"`
	Oldest time.Time `json:"oldest,omitzero"`
}

type storedFile struct {
	path    string
	project string
	day     string
	size    int64
}

func (s *Store) files() ([]storedFile, error) {
	var out []storedFile
	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".gz.tmp") {
			if info, err := d.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(path) // a compression a crash interrupted
			}
			return nil
		}
		m := dayFileRe.FindStringSubmatch(d.Name())
		rel, _ := filepath.Rel(s.dir, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if m == nil || len(parts) != 3 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, storedFile{path: path, project: parts[0], day: m[1], size: info.Size()})
		return nil
	})
	return out, err
}

// Usage sums up the stored files.
func (s *Store) Usage() (Usage, error) {
	files, err := s.files()
	if err != nil {
		return Usage{}, err
	}
	var u Usage
	oldest := ""
	for _, f := range files {
		u.Bytes += f.size
		u.Files++
		if oldest == "" || f.day < oldest {
			oldest = f.day
		}
	}
	if oldest != "" {
		u.Oldest, _ = time.Parse(dayLayout, oldest)
	}
	return u, nil
}

// PruneResult reports what a Prune removed.
type PruneResult struct {
	Compressed int
	Removed    int
	Freed      int64
}

// Prune compresses the days before yesterday, drops days older than keepDays, the
// oldest days beyond maxBytes (0 = no limit) and everything of projects keep rejects.
func (s *Store) Prune(now time.Time, keepDays int, maxBytes int64, keep func(project string) bool) (PruneResult, error) {
	var res PruneResult
	files, err := s.files()
	if err != nil {
		return res, err
	}
	// A day stays plain until the day after it is over, so late lines of a busy
	// container never land next to its compressed file.
	compressBefore := now.UTC().AddDate(0, 0, -1).Format(dayLayout)
	cutoff := now.UTC().AddDate(0, 0, -keepDays+1).Format(dayLayout)
	var kept []storedFile
	for _, f := range files {
		switch {
		case (keep != nil && !keep(f.project)) || (keepDays > 0 && f.day < cutoff):
			if err := os.Remove(f.path); err == nil {
				res.Removed++
				res.Freed += f.size
			}
			continue
		case !strings.HasSuffix(f.path, ".gz") && f.day < compressBefore:
			size, err := compress(f.path)
			if err != nil {
				return res, err
			}
			res.Compressed++
			res.Freed += f.size - size
			f.path += ".gz"
			f.size = size
		}
		kept = append(kept, f)
	}
	if maxBytes > 0 {
		var total int64
		for _, f := range kept {
			total += f.size
		}
		// Oldest day first, across all projects.
		sort.Slice(kept, func(i, j int) bool { return kept[i].day < kept[j].day })
		for _, f := range kept {
			if total <= maxBytes {
				break
			}
			if err := os.Remove(f.path); err == nil {
				res.Removed++
				res.Freed += f.size
				total -= f.size
			}
		}
	}
	s.removeEmptyDirs()
	return res, nil
}

// compress replaces path with path.gz (merging into an existing one) and returns the
// new file's size.
func compress(path string) (int64, error) {
	target := path + ".gz"
	tmp := target + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, err
	}
	zw := gzip.NewWriter(out)
	copyFrom := func(p string) error {
		r, err := openDayFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(zw, r)
		return err
	}
	err = copyFrom(target)
	if err == nil {
		err = copyFrom(path)
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, target)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("compress %s: %w", filepath.Base(path), err)
	}
	if err := os.Remove(path); err != nil {
		return 0, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *Store) removeEmptyDirs() {
	projects, _ := os.ReadDir(s.dir)
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		pdir := filepath.Join(s.dir, p.Name())
		services, _ := os.ReadDir(pdir)
		for _, sv := range services {
			_ = os.Remove(filepath.Join(pdir, sv.Name())) // only succeeds when empty
		}
		_ = os.Remove(pdir)
	}
}

// RemoveProject deletes a project's whole history.
func (s *Store) RemoveProject(id string) error {
	if !projectDirRe.MatchString(id) {
		return fmt.Errorf("%w: %s", ErrBadKey, id)
	}
	s.mu.Lock()
	for k := range s.last {
		if k.Project == id {
			delete(s.last, k)
		}
	}
	s.mu.Unlock()
	return os.RemoveAll(filepath.Join(s.dir, id))
}

// clearedMarker records when the history was last cleared, so lines Docker still holds
// from before are not collected again.
const clearedMarker = ".cleared"

// Clear deletes the whole history.
func (s *Store) Clear(now time.Time) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.dir, e.Name())); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(s.dir, clearedMarker), []byte(now.UTC().Format(time.RFC3339Nano)), 0o640)
}

// ClearedAt returns when the history was last cleared (zero = never).
func (s *Store) ClearedAt() time.Time {
	b, err := os.ReadFile(filepath.Join(s.dir, clearedMarker))
	if err != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
	return t
}
