package sagas

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"sync"
)

// ErrNoSecret is returned by SecretStore.Get when the store holds no value for the key.
var ErrNoSecret = &KindError{name: "no-secret", message: "sagas: no secret"}

// SecretKey names one secret value: a field of the event with this Seq, written by this writer of the execution.
type SecretKey struct {
	Execution string
	Writer    string
	Seq       int
	Field     string
}

// SecretStore keeps the values of the state fields tagged sagas:"secret", apart from the journal, each under its
// SecretKey. A Put on a held key replaces the value, as a resend does. A value survives a crash of the process once Put
// returns nil. Get returns an error that wraps ErrNoSecret for a key the store does not hold.
type SecretStore interface {
	Put(ctx context.Context, key SecretKey, value jsontext.Value) error
	Get(ctx context.Context, key SecretKey) (jsontext.Value, error)
}

// MemorySecretStore is a SecretStore that keeps a copy of every value in memory. Its zero value is ready to use.
type MemorySecretStore struct {
	mu     sync.Mutex
	values map[SecretKey]jsontext.Value
}

// NewMemorySecretStore returns an empty MemorySecretStore.
func NewMemorySecretStore() *MemorySecretStore {
	return &MemorySecretStore{values: map[SecretKey]jsontext.Value{}}
}

// Put implements SecretStore.
func (s *MemorySecretStore) Put(ctx context.Context, key SecretKey, value jsontext.Value) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.values == nil {
		s.values = map[SecretKey]jsontext.Value{}
	}

	s.values[key] = slices.Clone(value)

	return nil
}

// Get implements SecretStore.
func (s *MemorySecretStore) Get(ctx context.Context, key SecretKey) (jsontext.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	value, found := s.values[key]
	if !found {
		return nil, fmt.Errorf("%w: execution %q writer %q seq %d field %q",
			ErrNoSecret, key.Execution, key.Writer, key.Seq, key.Field)
	}

	return slices.Clone(value), nil
}
