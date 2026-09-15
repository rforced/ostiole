// Command ostiole-sign signs a file (checksums.txt) with the release
// ed25519 key from $OSTIOLE_SIGNING_KEY (base64 of the 64-byte private
// key) and writes a base64 signature. The updater verifies it with the
// embedded public key.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/rforced/ostiole/internal/update"
)

func main() {
	in := flag.String("in", "", "file to sign")
	out := flag.String("out", "", "signature file to write")
	genkey := flag.Bool("genkey", false, "generate a new release key pair and print both halves")
	public := flag.Bool("public", false, "print the public half of $OSTIOLE_SIGNING_KEY")
	flag.Parse()

	if *genkey {
		generate()
		return
	}
	if *public {
		printPublic()
		return
	}
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: ostiole-sign -in checksums.txt -out checksums.txt.sig")
		fmt.Fprintln(os.Stderr, "       ostiole-sign -genkey | -public")
		os.Exit(2)
	}
	key, err := signingKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sig := update.Sign(key, data)
	if err := os.WriteFile(*out, []byte(sig+"\n"), 0o644); err != nil { //nolint:gosec // release artifact
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// generate prints a fresh key pair: the secret for the repository, the
// public half for TrustedKeysHex. Rotating means keeping the old key in
// that list for a release, so boxes can verify the release that teaches
// them the new one.
func generate() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("secret (set as the OSTIOLE_SIGNING_KEY repository secret, and keep a backup):")
	fmt.Println(base64.StdEncoding.EncodeToString(priv))
	fmt.Println()
	fmt.Println("public (add to update.TrustedKeysHex before you sign with this key):")
	fmt.Println(hex.EncodeToString(pub))
}

// printPublic derives the public half of the configured secret, so a
// release can be checked against what the binaries trust.
func printPublic() {
	priv, err := signingKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		fmt.Fprintln(os.Stderr, "unexpected key type")
		os.Exit(1)
	}
	fmt.Println(hex.EncodeToString(pub))
}

func signingKey() (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("OSTIOLE_SIGNING_KEY")))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("OSTIOLE_SIGNING_KEY must be the base64 64-byte ed25519 private key")
	}
	return ed25519.PrivateKey(raw), nil
}
