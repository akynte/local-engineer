package task

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// randSuffix returns n lowercase base32 characters from a cryptographic
// source. Task ids appear in branch names and directory names, so they must
// not collide when two tasks start in the same second.
func randSuffix(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// A failure of the system RNG is not something to paper over with a
		// weaker source; a fixed suffix makes the collision visible instead.
		return strings.Repeat("0", n)
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return strings.ToLower(enc)[:n]
}
