// Package secrets encrypts the secrets Envoryx keeps at rest: passwords, tokens and keys
// in the database and in the JSON files under /config. A sealed value is
// "envoryx:v1:<key id>:<base64 of nonce and ciphertext>" (AES-256-GCM); a value without
// that prefix is plain text from before encryption, which Open returns as it is and
// Reseal encrypts.
//
// The key comes from ENVORYX_SECRET_KEY or, without it, from /config/secret.key, which
// Envoryx creates. One key ring serves the whole process (SetDefault); without one (tests,
// tools) Seal and Open pass values through unchanged.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// prefix starts every sealed value.
const prefix = "envoryx:v1:"

// KeySize is the length of a key in bytes.
const KeySize = 32

// ErrUnknownKey is returned for a value sealed with a key the ring does not hold.
var ErrUnknownKey = errors.New("the value was encrypted with a key Envoryx does not have")

// Key is one AES-256 key.
type Key struct {
	raw  []byte
	id   string
	aead cipher.AEAD
}

// NewKey wraps raw key bytes.
func NewKey(raw []byte) (*Key, error) {
	if len(raw) != KeySize {
		return nil, fmt.Errorf("a key has %d bytes, not %d", KeySize, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return &Key{raw: append([]byte(nil), raw...), id: hex.EncodeToString(sum[:4]), aead: aead}, nil
}

// GenerateKey returns a new random key.
func GenerateKey() (*Key, error) {
	raw := make([]byte, KeySize)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return NewKey(raw)
}

// ParseKey reads a key as written by Encode: base64 (standard or URL alphabet, with or
// without padding) or 64 hex digits.
func ParseKey(s string) (*Key, error) {
	s = strings.TrimSpace(s)
	if len(s) == 2*KeySize {
		if raw, err := hex.DecodeString(s); err == nil {
			return NewKey(raw)
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(s); err == nil && len(raw) == KeySize {
			return NewKey(raw)
		}
	}
	return nil, fmt.Errorf("not a key: expected %d bytes as base64 or 64 hex digits (create one with: openssl rand -base64 32)", KeySize)
}

// ID is a short fingerprint of the key (not secret).
func (k *Key) ID() string { return k.id }

// Encode returns the key as base64, the form ParseKey reads and the UI shows.
func (k *Key) Encode() string { return base64.StdEncoding.EncodeToString(k.raw) }

// Ring holds the key new values are sealed with and older keys that still open values.
type Ring struct {
	mu      sync.RWMutex
	current *Key
	old     []*Key
}

// NewRing returns a ring sealing with current and opening with current and old.
func NewRing(current *Key, old ...*Key) *Ring {
	r := &Ring{current: current}
	for _, k := range old {
		if k != nil && k.id != current.id {
			r.old = append(r.old, k)
		}
	}
	return r
}

// Current returns the key values are sealed with.
func (r *Ring) Current() *Key {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

// Has reports whether the ring opens values of the key id.
func (r *Ring) Has(id string) bool { return r.key(id) != nil }

func (r *Ring) key(id string) *Key {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.current.id == id {
		return r.current
	}
	for _, k := range r.old {
		if k.id == id {
			return k
		}
	}
	return nil
}

// Rotate makes next the current key; the previous one keeps opening values until
// DropOld.
func (r *Ring) Rotate(next *Key) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if next.id == r.current.id {
		return
	}
	r.old = append([]*Key{r.current}, r.old...)
	r.current = next
}

// Add lets the ring open values of k without sealing with it.
func (r *Ring) Add(k *Key) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k.id == r.current.id {
		return
	}
	for _, o := range r.old {
		if o.id == k.id {
			return
		}
	}
	r.old = append(r.old, k)
}

// DropOld forgets every key but the current one (after everything was resealed).
func (r *Ring) DropOld() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.old = nil
}

// Seal encrypts s with the current key; the empty string stays empty.
func (r *Ring) Seal(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	k := r.Current()
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	out := k.aead.Seal(nonce, nonce, []byte(s), nil)
	return prefix + k.id + ":" + base64.RawStdEncoding.EncodeToString(out), nil
}

// Open decrypts a sealed value; plain text is returned unchanged.
func (r *Ring) Open(s string) (string, error) {
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return s, nil
	}
	id, body, ok := strings.Cut(rest, ":")
	if !ok {
		return "", errors.New("open: malformed sealed value")
	}
	k := r.key(id)
	if k == nil {
		return "", fmt.Errorf("%w (key %s)", ErrUnknownKey, id)
	}
	raw, err := base64.RawStdEncoding.DecodeString(body)
	if err != nil || len(raw) < k.aead.NonceSize() {
		return "", errors.New("open: malformed sealed value")
	}
	n := k.aead.NonceSize()
	plain, err := k.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", fmt.Errorf("open: the value does not decrypt with key %s (damaged?)", id)
	}
	return string(plain), nil
}

// NeedsReseal reports whether s is plain text or sealed with another than the current key.
func (r *Ring) NeedsReseal(s string) bool {
	if s == "" {
		return false
	}
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return true
	}
	id, _, _ := strings.Cut(rest, ":")
	return id != r.Current().id
}

// Reseal returns s sealed with the current key and whether it changed.
func (r *Ring) Reseal(s string) (string, bool, error) {
	if !r.NeedsReseal(s) {
		return s, false, nil
	}
	plain, err := r.Open(s)
	if err != nil {
		return s, false, err
	}
	out, err := r.Seal(plain)
	return out, err == nil, err
}

// IsSealed reports whether s is a sealed value.
func IsSealed(s string) bool { return strings.HasPrefix(s, prefix) }

// KeyIDOf returns the key id a sealed value names ("" for plain text).
func KeyIDOf(s string) string {
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, ":")
	return id
}

// ---- the process's ring -------------------------------------------------------------

var (
	defaultMu   sync.RWMutex
	defaultRing *Ring
)

// SetDefault installs the process's ring (nil switches encryption off, for tests).
func SetDefault(r *Ring) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultRing = r
}

// Default returns the process's ring, nil when none is installed.
func Default() *Ring {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultRing
}

// Seal seals s with the process's ring; without one it returns s.
func Seal(s string) (string, error) {
	if r := Default(); r != nil {
		return r.Seal(s)
	}
	return s, nil
}

// Open opens s with the process's ring; without one a sealed value is an error.
func Open(s string) (string, error) {
	if r := Default(); r != nil {
		return r.Open(s)
	}
	if IsSealed(s) {
		return "", fmt.Errorf("%w (no key loaded)", ErrUnknownKey)
	}
	return s, nil
}
