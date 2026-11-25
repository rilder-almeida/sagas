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

// SecretStore keeps the values of the state fields tagged sagas:"secret", apart from the journal. A value is keyed by
// the execution, the Seq of the event that names the field and the field. Get returns an error that wraps ErrNoSecret
// for a key the store does not hold.
type SecretStore interface {
	Put(ctx context.Context, execution string, seq int, field string, value jsontext.Value) error
	Get(ctx context.Context, execution string, seq int, field string) (jsontext.Value, error)
}

type secretKey struct {
	execution string
	seq       int
	field     string
}

// MemorySecretStore is a SecretStore that keeps a copy of every value in memory. Its zero value is ready to use.
type MemorySecretStore struct {
	mu     sync.Mutex
	values map[secretKey]jsontext.Value
}

// NewMemorySecretStore returns an empty MemorySecretStore.
func NewMemorySecretStore() *MemorySecretStore {
	return &MemorySecretStore{values: map[secretKey]jsontext.Value{}}
}

// Put implements SecretStore.
func (s *MemorySecretStore) Put(
	ctx context.Context, execution string, seq int, field string, value jsontext.Value,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.values == nil {
		s.values = map[secretKey]jsontext.Value{}
	}

	s.values[secretKey{execution: execution, seq: seq, field: field}] = slices.Clone(value)

	return nil
}

// Get implements SecretStore.
func (s *MemorySecretStore) Get(ctx context.Context, execution string, seq int, field string) (jsontext.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	value, found := s.values[secretKey{execution: execution, seq: seq, field: field}]
	if !found {
		return nil, fmt.Errorf("%w: execution %q seq %d field %q", ErrNoSecret, execution, seq, field)
	}

	return slices.Clone(value), nil
}
