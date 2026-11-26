package sagastest

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"testing"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noSentinelStore struct {
	*sagas.MemorySecretStore

	mu   sync.Mutex
	held map[string]bool
}

func (s *noSentinelStore) Put(ctx context.Context, key sagas.SecretKey, value jsontext.Value) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.held[key.Execution] = true

	return s.MemorySecretStore.Put(ctx, key, value)
}

func (s *noSentinelStore) Get(ctx context.Context, key sagas.SecretKey) (jsontext.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	value, err := s.MemorySecretStore.Get(ctx, key)
	if errors.Is(err, sagas.ErrNoSecret) && s.held[key.Execution] {
		return nil, nil
	}

	return value, err
}

type alwaysNilStore struct{ *sagas.MemorySecretStore }

func (alwaysNilStore) Get(context.Context, sagas.SecretKey) (jsontext.Value, error) {
	return nil, nil
}

type keepsFirstStore struct {
	*sagas.MemorySecretStore

	mu sync.Mutex
}

func (s *keepsFirstStore) Put(ctx context.Context, key sagas.SecretKey, value jsontext.Value) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.Get(ctx, key); err == nil {
		return nil
	}

	return s.MemorySecretStore.Put(ctx, key, value)
}

type changesValueStore struct{ *sagas.MemorySecretStore }

func (s changesValueStore) Get(ctx context.Context, key sagas.SecretKey) (jsontext.Value, error) {
	value, err := s.MemorySecretStore.Get(ctx, key)

	return append(value, ' '), err
}

type failsPutStore struct{ *sagas.MemorySecretStore }

func (failsPutStore) Put(context.Context, sagas.SecretKey, jsontext.Value) error {
	return fs.ErrPermission
}

type deniesGetStore struct{ *sagas.MemorySecretStore }

func (deniesGetStore) Get(context.Context, sagas.SecretKey) (jsontext.Value, error) {
	return nil, fs.ErrPermission
}

func holdingSecret(t *testing.T) *sagas.MemorySecretStore {
	t.Helper()

	s := sagas.NewMemorySecretStore()
	require.NoError(t, s.Put(t.Context(), secretKey(fieldHeld), jsontext.Value(`"held"`)))

	return s
}

func secretRules() []string {
	return []string{ruleMissingKey, ruleStoredValue, ruleReplacingPut}
}

func Test_SecretStoreCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		newStore     func(*testing.T) sagas.SecretStore
		wantProbeErr bool
		wantFailing  []string
		wantWrapped  []error
	}{
		{
			name:     "[Success] When: the store is the memory store ; Should: report nothing",
			newStore: func(*testing.T) sagas.SecretStore { return sagas.NewMemorySecretStore() },
		},
		{
			name: "[Fail] When: Get returns nil and a nil error for a missing key beside a held key ; " +
				"Should: report the missing key",
			newStore: func(*testing.T) sagas.SecretStore {
				return &noSentinelStore{MemorySecretStore: sagas.NewMemorySecretStore(), held: map[string]bool{}}
			},
			wantFailing: []string{ruleMissingKey},
		},
		{
			name: "[Fail] When: a second Put keeps the first value ; Should: report the replacing put",
			newStore: func(*testing.T) sagas.SecretStore {
				return &keepsFirstStore{MemorySecretStore: sagas.NewMemorySecretStore()}
			},
			wantFailing: []string{ruleReplacingPut},
		},
		{
			name: "[Fail] When: Get returns another value ; " +
				"Should: report the stored value and the replacing put",
			newStore:    func(*testing.T) sagas.SecretStore { return changesValueStore{sagas.NewMemorySecretStore()} },
			wantFailing: []string{ruleStoredValue, ruleReplacingPut},
		},
		{
			name:        "[Fail] When: Put returns an error ; Should: report every case with the error as its cause",
			newStore:    func(*testing.T) sagas.SecretStore { return failsPutStore{sagas.NewMemorySecretStore()} },
			wantFailing: []string{ruleMissingKey, ruleStoredValue, ruleReplacingPut},
			wantWrapped: []error{fs.ErrPermission},
		},
		{
			name:         "[Fail] When: Get returns nil and a nil error on an empty store ; Should: refuse the probe",
			newStore:     func(*testing.T) sagas.SecretStore { return alwaysNilStore{sagas.NewMemorySecretStore()} },
			wantProbeErr: true,
		},
		{
			name: "[Fail] When: Get returns another error ; " +
				"Should: refuse the probe with the error as its cause",
			newStore:     func(*testing.T) sagas.SecretStore { return deniesGetStore{sagas.NewMemorySecretStore()} },
			wantProbeErr: true,
			wantWrapped:  []error{fs.ErrPermission},
		},
		{
			name: "[Fail] When: the probe holds a value ; Should: refuse the probe before any case",
			newStore: func(t *testing.T) sagas.SecretStore {
				t.Helper()

				return holdingSecret(t)
			},
			wantProbeErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cases, err := SecretStoreCases(t.Context(), tt.newStore(t))
			if tt.wantProbeErr {
				require.ErrorIs(t, err, ErrSecretStoreContract)
				assert.Nil(t, cases)
				assert.Empty(t, tt.wantFailing)
				assertWrapped(t, tt.wantWrapped, err)

				return
			}

			require.NoError(t, err)

			var (
				names, failing []string
				runErrs        []error
			)

			for c := range cases {
				names = append(names, c.Name)

				if runErr := c.Run(t.Context(), tt.newStore(t)); runErr != nil {
					assert.ErrorIs(t, runErr, ErrSecretStoreContract)
					assert.Contains(t, runErr.Error(), fmt.Sprintf("rule %q", c.Name))

					failing = append(failing, c.Name)
					runErrs = append(runErrs, runErr)
				}
			}

			assert.Equal(t, secretRules(), names)
			assert.Equal(t, tt.wantFailing, failing)
			assertWrapped(t, tt.wantWrapped, errors.Join(runErrs...))
		})
	}
}

func Test_TestSecretStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		newStore func() sagas.SecretStore
	}{
		{
			name: "[Success] When: the store is the memory store ; " +
				"Should: call the factory with the parent t and then once per case with the subtest t",
			newStore: func() sagas.SecretStore { return sagas.NewMemorySecretStore() },
		},
		{
			name: "[Success] When: the store is the zero memory store ; " +
				"Should: call the factory with the parent t and then once per case with the subtest t",
			newStore: func() sagas.SecretStore { return &sagas.MemorySecretStore{} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			log := &factoryLog{}

			TestSecretStore(t, func(t *testing.T) sagas.SecretStore {
				t.Helper()
				log.called(t.Name())

				return tt.newStore()
			})

			assert.Equal(t, subtestNames(t.Name(), secretRules()), log.names)
		})
	}
}
