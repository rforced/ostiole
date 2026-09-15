// Command ostiole-sign signs a file (checksums.txt) with the release
// ed25519 key from $OSTIOLE_SIGNING_KEY (base64 of the 64-byte private
// key) and writes a base64 signature. The updater verifies it with the
// embedded public key.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/rforced/ostiole/internal/update"
)

func main() {
	in := flag.String("in", "", "file to sign")
	out := flag.String("out", "", "signature file to write")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: ostiole-sign -in checksums.txt -out checksums.txt.sig")
		os.Exit(2)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("OSTIOLE_SIGNING_KEY")))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		fmt.Fprintln(os.Stderr, "OSTIOLE_SIGNING_KEY must be the base64 64-byte ed25519 private key")
		os.Exit(2)
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sig := update.Sign(ed25519.PrivateKey(raw), data)
	if err := os.WriteFile(*out, []byte(sig+"\n"), 0o644); err != nil { //nolint:gosec // release artifact
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
