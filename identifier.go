package sagas

import (
	"crypto/sha1"
	"encoding/hex"
	"time"
)

// Identifier is an interface that represents an identifier of a step. It is used to
// identify the step and it is unique.
type Identifier interface {
	// String is a method that returns the string representation of the identifier.
	String() string
}

// identifier is a string that represents the identifier of step. It is used to
// identify the step and it is unique. This is a concrete implementation of the
// Identifier interface.
type identifier string

// NewIdentifier is a function that creates a new identifier. It receives a name
// as parameter and returns an identifier. Example:
//
//	identifier := sagas.NewIdentifier("step")
//
// The identifier will be a string in the format "step:unique_identifier".
func NewIdentifier(name string) Identifier {
	return identifier(name + ":" + makeUniqueIdentifier(name)[0:12])
}

// String is a method that returns the string representation of the identifier.
func (i identifier) String() string {
	return string(i)
}

// MakeUniqueIdentifier returns a unique identifier for a given string.
func makeUniqueIdentifier(s string) string {
	h := sha1.New()
	s = time.Now().Format(time.RFC850) + s
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
