package offsite

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

// hostKeyPin is what an SFTP target's HostKey names, one key per line: SHA256
// fingerprints, or whole public keys as ssh-keyscan, known_hosts or a .pub file print
// them. A whole key also gives its type, so the handshake can ask for that type; a server
// offering several keys would otherwise hand over the one its client likes best.
type hostKeyPin struct {
	fingerprints []string
	// types of the pinned keys; empty when one of them is a bare fingerprint.
	types []string
}

func (p hostKeyPin) empty() bool { return len(p.fingerprints) == 0 }

func (p hostKeyPin) matches(fp string) bool { return slices.Contains(p.fingerprints, fp) }

func (p hostKeyPin) String() string { return strings.Join(p.fingerprints, ", ") }

// algorithms are the host key algorithms for the pinned types; nil when a type isn't known.
func (p hostKeyPin) algorithms() []string {
	var out []string
	for _, t := range p.types {
		out = append(out, keyAlgorithms(t)...)
	}
	return out
}

// keyAlgorithms are the signature algorithms a host key of type t signs the handshake with.
func keyAlgorithms(t string) []string {
	if t == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{t}
}

// hostKeyTypes are the key types tried one by one when only a fingerprint is pinned.
var hostKeyTypes = []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSA}

var errHostKeyFormat = errors.New("the server key must be a SHA256 fingerprint (SHA256:…) or a public key as ssh-keyscan prints it (ssh-ed25519 AAAA…)")

func parseHostKey(s string) (hostKeyPin, error) {
	var p hostKeyPin
	typed := true
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, ok := parseKeyLine(line); ok {
			p.fingerprints = append(p.fingerprints, ssh.FingerprintSHA256(key))
			if !slices.Contains(p.types, key.Type()) {
				p.types = append(p.types, key.Type())
			}
			continue
		}
		// A fingerprint, alone or as ssh-keygen -l prints it ("256 SHA256:… host (ED25519)").
		found := false
		for f := range strings.FieldsSeq(line) {
			if fp, ok := strings.CutPrefix(f, "SHA256:"); ok {
				if b, err := base64.RawStdEncoding.DecodeString(fp); err != nil || len(b) != 32 {
					return hostKeyPin{}, errHostKeyFormat
				}
				p.fingerprints = append(p.fingerprints, f)
				found = true
			}
		}
		if !found {
			return hostKeyPin{}, errHostKeyFormat
		}
		typed = false
	}
	if !typed {
		p.types = nil
	}
	return p, nil
}

func parseKeyLine(line string) (ssh.PublicKey, bool) {
	if key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err == nil {
		return key, true
	}
	if _, _, key, _, _, err := ssh.ParseKnownHosts([]byte(line)); err == nil {
		return key, true
	}
	return nil, false
}

// hostKeyMismatch is the error for a server whose key isn't the pinned one.
func hostKeyMismatch(p hostKeyPin, got []ssh.PublicKey) error {
	seen := make([]string, len(got))
	for i, k := range got {
		seen[i] = ssh.FingerprintSHA256(k) + " (" + k.Type() + ")"
	}
	return fmt.Errorf("%w: expected %s, got %s - if the server was replaced on purpose, clear the pinned key in the target", ErrHostKeyChanged, p, strings.Join(seen, ", "))
}
