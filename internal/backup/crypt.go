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

// WorkFactor sets the scrypt cost of a new file to 2^WorkFactor; zero
// keeps age's own, 18. Only tests change it: under the race detector
// age's cost is seconds a file.
var WorkFactor int

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
	if WorkFactor > 0 {
		rec.SetWorkFactor(WorkFactor)
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

// maxWorkFactor is the most scrypt work a file may ask for: 2^18, what
// Encrypt and age(1) write, 256 MiB and about a second. The file sets
// it, and age would otherwise take 2^22 from anyone who uploads one:
// 4 GiB before the passphrase is even checked.
const maxWorkFactor = 18

// opening holds a slot for each file being opened, so a burst of uploads
// waits its turn rather than taking a quarter of a gigabyte each.
var opening = make(chan struct{}, 2)

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
	id.SetMaxWorkFactor(maxWorkFactor)
	opening <- struct{}{}
	r, err := age.Decrypt(bytes.NewReader(raw), id)
	<-opening
	if err != nil {
		// age says "no identity matched any of the recipients", or
		// "identity did not match any of the recipients" depending on its
		// version, for a wrong passphrase. Neither is a sentence anybody
		// wants.
		text := err.Error()
		switch {
		case strings.Contains(text, "no identity matched"), strings.Contains(text, "identity did not match"):
			return nil, ErrBadPassphrase
		case strings.Contains(text, "work factor too large"):
			return nil, ErrTooCostly
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
