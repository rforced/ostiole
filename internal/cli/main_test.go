package cli

import (
	"testing"

	"ostiole/internal/auth"
)

// Under the race detector the router's password hashing costs every
// command that makes or checks an account.
func TestMain(m *testing.M) {
	auth.HashMemory = 64
	m.Run()
}
