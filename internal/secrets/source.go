package secrets

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Environment variables and files that hold keys.
const (
	// EnvKey is the key itself; it wins over the key file.
	EnvKey = "ENVORYX_SECRET_KEY"
	// EnvOldKey is the previous key, set for one start after changing EnvKey.
	EnvOldKey = "ENVORYX_SECRET_KEY_OLD"
	// KeyFile is created in the config directory when EnvKey is not set.
	KeyFile = "secret.key"
	// NextKeyFile holds the new key while a rotation runs.
	NextKeyFile = "secret.key.next"
	// OldKeyFile holds the previous key at the end of a rotation.
	OldKeyFile = "secret.key.old"
	// RestoreKeyFile holds the key of an instance backup restored from another key.
	RestoreKeyFile = "secret.key.restore"
)

// KeyFiles are the files in the config directory that hold keys; instance backups leave
// them out.
var KeyFiles = []string{KeyFile, NextKeyFile, OldKeyFile, RestoreKeyFile}

// Source says where the current key comes from.
type Source struct {
	// Kind is "env" or "file".
	Kind string `json:"kind"`
	// Path is the key file (Kind file).
	Path string `json:"path,omitempty"`
	// Created is set when there was no key file: a new key was generated, which Persist
	// writes once it is known to fit the database.
	Created bool `json:"created,omitempty"`
	dir     string
	extra   []string // files whose keys only open values
	created *Key
}

// Persist writes a key generated at this start to the key file (a no-op otherwise). It
// is called after the key was checked against the database, so a start without the right
// key leaves no stray key file behind.
func (s Source) Persist() error {
	if s.created == nil {
		return nil
	}
	return WriteKeyFile(filepath.Join(s.dir, KeyFile), s.created)
}

// Load builds the ring: the current key from EnvKey or the key file (generated when
// missing; see Source.Persist), plus every key that may still have sealed values - EnvOldKey, a key file
// the environment variable replaces, and the files a rotation or a restore left.
func Load(configDir string, getenv func(string) string) (*Ring, Source, error) {
	src := Source{dir: configDir}
	path := func(name string) string { return filepath.Join(configDir, name) }
	var current *Key
	var extra []*Key

	readKey := func(name string) (*Key, error) {
		raw, err := os.ReadFile(path(name))
		if err != nil {
			return nil, err
		}
		k, err := ParseKey(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path(name), err)
		}
		return k, nil
	}
	addFile := func(name string) error {
		k, err := readKey(name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		extra = append(extra, k)
		src.extra = append(src.extra, name)
		return nil
	}

	if v := strings.TrimSpace(getenv(EnvKey)); v != "" {
		k, err := ParseKey(v)
		if err != nil {
			return nil, src, fmt.Errorf("%s: %w", EnvKey, err)
		}
		current, src.Kind = k, "env"
		// A key file from before the variable still opens values; resealing retires it.
		if err := addFile(KeyFile); err != nil {
			return nil, src, err
		}
	} else {
		src.Kind, src.Path = "file", path(KeyFile)
		// A rotation that stopped after moving the old key aside finishes here.
		if _, err := os.Stat(path(KeyFile)); errors.Is(err, fs.ErrNotExist) {
			if _, err := os.Stat(path(NextKeyFile)); err == nil {
				if err := os.Rename(path(NextKeyFile), path(KeyFile)); err != nil {
					return nil, src, err
				}
			}
		}
		k, err := readKey(KeyFile)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if k, err = GenerateKey(); err != nil {
				return nil, src, err
			}
			src.Created, src.created = true, k
		case err != nil:
			return nil, src, err
		}
		current = k
	}
	if v := strings.TrimSpace(getenv(EnvOldKey)); v != "" {
		k, err := ParseKey(v)
		if err != nil {
			return nil, src, fmt.Errorf("%s: %w", EnvOldKey, err)
		}
		extra = append(extra, k)
	}
	for _, name := range []string{NextKeyFile, OldKeyFile, RestoreKeyFile} {
		if err := addFile(name); err != nil {
			return nil, src, err
		}
	}
	return NewRing(current, extra...), src, nil
}

// Cleanup removes the key files whose values were all resealed with the current key: the
// leftovers of a rotation or a restore, and a key file the environment variable replaced.
func (s Source) Cleanup() []string {
	var removed []string
	for _, name := range s.extra {
		if s.Kind == "file" && name == KeyFile {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err == nil {
			removed = append(removed, name)
		}
	}
	return removed
}

// WriteKeyFile writes a key file (0600) atomically.
func WriteKeyFile(path string, k *Key) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(k.Encode()+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ---- files --------------------------------------------------------------------------

// ReadFile reads a file that may be sealed as a whole (see WriteFile).
func ReadFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(raw))
	if !IsSealed(s) {
		return raw, nil
	}
	plain, err := Open(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []byte(plain), nil
}

// WriteFile seals data with the process's ring and writes it atomically.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	sealed, err := Seal(string(data))
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sealed), perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ResealFile seals a plain file, or one sealed with an older key, with the current key.
// A missing file is fine.
func ResealFile(path string) (bool, error) {
	r := Default()
	if r == nil {
		return false, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s := string(raw)
	if IsSealed(strings.TrimSpace(s)) {
		s = strings.TrimSpace(s)
	}
	if !r.NeedsReseal(s) {
		return false, nil
	}
	plain, err := r.Open(s)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return true, WriteFile(path, []byte(plain), info.Mode().Perm())
}
