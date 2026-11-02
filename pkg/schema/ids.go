package schema

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a random 16-character hex identifier.
func NewID() string {
	var b [8]byte
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
