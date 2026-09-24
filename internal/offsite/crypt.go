package offsite

import (
	"errors"
	"fmt"
	"io"

	"filippo.io/age"
)

// Encrypted archives are standard age files with a scrypt passphrase recipient, so they
// can be opened without Envoryx: age -d -o backup.tar backup.tar.age.

// encryptedSuffix marks encrypted objects; listing tells them apart by name.
const encryptedSuffix = ".age"

// seal returns a writer that encrypts into w; closing it finishes the age stream (not w).
func seal(w io.Writer, passphrase string) (io.WriteCloser, error) {
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	return age.Encrypt(w, r)
}

// ErrPassphrase is returned when an archive cannot be decrypted with the passphrase.
var ErrPassphrase = errors.New("the archive cannot be decrypted with this target's passphrase")

// unseal decrypts r.
func unseal(r io.Reader, passphrase string) (io.Reader, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("%w (the target has none)", ErrPassphrase)
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	out, err := age.Decrypt(r, id)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ErrPassphrase
		}
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return out, nil
}
