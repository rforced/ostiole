package backup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// Header is the first line of an encrypted file, which is how one is
// recognised without being decrypted.
const Header = "age-encryption.org/v1"

// Suffix is what an encrypted backup is called. `age -d` opens a file
// with this name on any machine, with nothing from Ostiole installed.
const Suffix = ".age"

// IsEncrypted reports whether raw is an age file rather than JSON.
func IsEncrypted(raw []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(raw, "\n"), []byte(Header))
}

// Encrypt wraps raw in an age file locked with a passphrase. The binary
// format is used rather than armour: a backup is downloaded, not pasted.
func Encrypt(raw []byte, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("a passphrase is required to encrypt a backup")
	}
	rec, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	w, err := age.Encrypt(&out, rec)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Decrypt opens an age file with a passphrase. A file that is not
// encrypted at all is returned as it came, so a caller can hand over
// whatever was uploaded.
func Decrypt(raw []byte, passphrase string) ([]byte, error) {
	if !IsEncrypted(raw) {
		return raw, nil
	}
	if passphrase == "" {
		return nil, ErrPassphraseNeeded
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(raw), id)
	if err != nil {
		// age says "no identity matched any of the recipients", or
		// "identity did not match any of the recipients" depending on its
		// version, for a wrong passphrase. Neither is a sentence anybody
		// wants.
		if text := err.Error(); strings.Contains(text, "no identity matched") ||
			strings.Contains(text, "identity did not match") {
			return nil, ErrBadPassphrase
		}
		return nil, fmt.Errorf("%w: %w", ErrBadPassphrase, err)
	}
	out, err := io.ReadAll(io.LimitReader(r, maxPlaintextBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadPassphrase, err)
	}
	return out, nil
}

// maxPlaintextBytes bounds what an encrypted file may expand to, so a
// file built to exhaust memory cannot.
const maxPlaintextBytes = 64 << 20
