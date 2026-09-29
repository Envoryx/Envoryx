package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/secrets"
	"github.com/envoryx/envoryx/internal/validate"
)

// The secrets Envoryx keeps at rest are sealed with one key (package secrets): in the
// database, in the configuration files below and in the project export of every
// backup.json. Resealing converts plain values and values of older keys to the current
// key; it runs at every start and after a rotation.

// sealedConfigFiles are the files under /config whose content is sealed as a whole.
var sealedConfigFiles = []string{"notify.json", "offsite.json", filepath.Join("ca", "acme.json")}

// SecretKeyInfo describes the key for the settings.
type SecretKeyInfo struct {
	// Source is "env" (ENVORYX_SECRET_KEY) or "file" (/config/secret.key).
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	KeyID  string `json:"keyId"`
	// CanRotate is false with the environment variable: its value changes outside.
	CanRotate bool   `json:"canRotate"`
	EnvKey    string `json:"envKey"`
	EnvOldKey string `json:"envOldKey"`
}

// ResealReport counts what a reseal changed.
type ResealReport struct {
	Database int `json:"database"`
	Files    int `json:"files"`
	Backups  int `json:"backups"`
}

// SetSecretKeySource records where the process's key came from.
func (m *Manager) SetSecretKeySource(src secrets.Source) { m.keySource = src }

// SecretKeyInfo describes the process's key.
func (m *Manager) SecretKeyInfo() SecretKeyInfo {
	info := SecretKeyInfo{Source: m.keySource.Kind, Path: m.keySource.Path, EnvKey: secrets.EnvKey, EnvOldKey: secrets.EnvOldKey}
	if r := secrets.Default(); r != nil {
		info.KeyID = r.Current().ID()
	}
	info.CanRotate = info.Source == "file" && info.KeyID != ""
	return info
}

// RevealSecretKey returns the current key, for the admin to keep it safe. The reveal is
// audited.
func (m *Manager) RevealSecretKey(ctx context.Context) (string, error) {
	r := secrets.Default()
	if r == nil {
		return "", fmt.Errorf("%w: no key is loaded", ErrNotConfigured)
	}
	m.audit.Log(ctx, audit.ActionSecretKeyRevealed, "settings", "", map[string]any{"keyId": r.Current().ID()})
	return r.Current().Encode(), nil
}

// ResealAll seals every secret at rest with the current key.
func (m *Manager) ResealAll(ctx context.Context) (ResealReport, error) {
	var rep ResealReport
	n, err := m.store.ResealSecrets(ctx)
	if err != nil {
		return rep, err
	}
	rep.Database = n
	if p, err := m.sealRoots(); err == nil {
		for _, f := range sealedConfigFiles {
			changed, err := secrets.ResealFile(filepath.Join(p.ConfigDir, f))
			if err != nil {
				return rep, err
			}
			if changed {
				rep.Files++
			}
		}
	}
	if rep.Backups, err = m.ResealBackups(ctx); err != nil {
		return rep, err
	}
	return rep, nil
}

// RotateSecretKey replaces the key file's key with a new one and reseals everything. The
// new key waits in secret.key.next until all is resealed, so a crash on the way leaves
// both keys at hand for the next start. Instance backups made before keep needing the
// old key.
func (m *Manager) RotateSecretKey(ctx context.Context) (SecretKeyInfo, ResealReport, error) {
	info := m.SecretKeyInfo()
	if !info.CanRotate {
		return info, ResealReport{}, fmt.Errorf("%w: the key comes from %s; set a new key there and the old one as %s for one start", validate.ErrInvalid, secrets.EnvKey, secrets.EnvOldKey)
	}
	if _, err := m.sealRoots(); err != nil {
		// Without them the files and backups could not be resealed.
		return info, ResealReport{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	m.rotateMu.Lock()
	defer m.rotateMu.Unlock()
	r := secrets.Default()
	old := r.Current()
	next, err := secrets.GenerateKey()
	if err != nil {
		return info, ResealReport{}, err
	}
	dir := filepath.Dir(info.Path)
	if err := secrets.WriteKeyFile(filepath.Join(dir, secrets.NextKeyFile), next); err != nil {
		return info, ResealReport{}, err
	}
	r.Rotate(next)
	rep, err := m.ResealAll(ctx)
	if err != nil {
		// The old key opens everything that was not resealed yet, and the next start
		// finishes the job with both keys.
		return info, rep, fmt.Errorf("resealing with the new key failed, the next start retries: %w", err)
	}
	if err := secrets.WriteKeyFile(info.Path, next); err != nil {
		return info, rep, err
	}
	_ = os.Remove(filepath.Join(dir, secrets.NextKeyFile))
	// Everything is sealed with the new key now; the old one must not seem to be at hand
	// (a restore of an older instance backup has to ask for it).
	r.DropOld()
	m.audit.Log(ctx, audit.ActionSecretKeyRotated, "settings", "", map[string]any{"from": old.ID(), "to": next.ID(), "database": rep.Database, "files": rep.Files, "backups": rep.Backups})
	return m.SecretKeyInfo(), rep, nil
}

// sealRoots returns the directories resealing works in: from the configuration when it
// names them (no host path detection needed), else from the paths.
func (m *Manager) sealRoots() (Paths, error) {
	if m.cfg.ConfigDir != "" {
		return Paths{ConfigDir: m.cfg.ConfigDir, BackupsDir: m.cfg.BackupsDir}, nil
	}
	return m.paths()
}
