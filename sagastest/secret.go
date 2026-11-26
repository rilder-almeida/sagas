package sagastest

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"iter"
	"testing"

	"github.com/rilder-almeida/sagas"
)

const (
	ruleFreshStore   = "fresh store"
	ruleMissingKey   = "missing key"
	ruleStoredValue  = "stored value"
	ruleReplacingPut = "replacing put"
	fieldHeld        = "Secret"
	fieldMissing     = "Other"
)

func secretKey(field string) sagas.SecretKey {
	return sagas.SecretKey{Execution: executionMain, Writer: "writer", Seq: 1, Field: field}
}

// SecretStoreCases checks that probe holds no value for the keys of the cases, then returns the cases missing key,
// stored value and replacing put. Each case needs a fresh empty store. Each error wraps ErrSecretStoreContract.
func SecretStoreCases(ctx context.Context, probe sagas.SecretStore) (iter.Seq[Case[sagas.SecretStore]], error) {
	if err := freshStore(ctx, probe); err != nil {
		return nil, err
	}

	return freshCases(freshStore, []Case[sagas.SecretStore]{
		{Name: ruleMissingKey, run: missingKeyCase},
		{Name: ruleStoredValue, run: storedValueCase},
		{Name: ruleReplacingPut, run: replacingPutCase},
	}), nil
}

// TestSecretStore runs each case of SecretStoreCases in a subtest named after it. It calls newStore with t for the
// probe and with the t of the subtest for each case. Each call must return a fresh empty store.
func TestSecretStore(t *testing.T, newStore func(*testing.T) sagas.SecretStore) {
	t.Helper()

	cases, err := SecretStoreCases(t.Context(), newStore(t))
	if err != nil {
		t.Fatal(err)
	}

	runCases(t, cases, newStore)
}

func freshStore(ctx context.Context, s sagas.SecretStore) error {
	return errors.Join(
		wantNoSecret(ctx, ruleFreshStore, s, secretKey(fieldHeld)),
		wantNoSecret(ctx, ruleFreshStore, s, secretKey(fieldMissing)),
	)
}

func wantNoSecret(ctx context.Context, rule string, s sagas.SecretStore, key sagas.SecretKey) error {
	value, err := s.Get(ctx, key)
	if !errors.Is(err, sagas.ErrNoSecret) {
		return ruleFailure(ErrSecretStoreContract, rule, fmt.Sprintf(
			"Get of field %q returned value %q, want an error wrapping %v", key.Field, value, sagas.ErrNoSecret), err)
	}

	return nil
}

func wantSecret(ctx context.Context, rule string, s sagas.SecretStore, want jsontext.Value) error {
	got, err := s.Get(ctx, secretKey(fieldHeld))
	if err != nil || !bytes.Equal(got, want) {
		return ruleFailure(ErrSecretStoreContract, rule,
			fmt.Sprintf("Get of field %q returned value %q, want %q and no error", fieldHeld, got, want), err)
	}

	return nil
}

func putSecret(ctx context.Context, rule string, s sagas.SecretStore, value jsontext.Value) error {
	if err := s.Put(ctx, secretKey(fieldHeld), value); err != nil {
		return ruleFailure(ErrSecretStoreContract, rule,
			fmt.Sprintf("Put of %q returned an error, want nil", value), err)
	}

	return nil
}

func missingKeyCase(ctx context.Context, s sagas.SecretStore) error {
	return errors.Join(
		putSecret(ctx, ruleMissingKey, s, jsontext.Value(`"first"`)),
		wantNoSecret(ctx, ruleMissingKey, s, secretKey(fieldMissing)),
	)
}

func storedValueCase(ctx context.Context, s sagas.SecretStore) error {
	return errors.Join(
		putSecret(ctx, ruleStoredValue, s, jsontext.Value(`"first"`)),
		wantSecret(ctx, ruleStoredValue, s, jsontext.Value(`"first"`)),
	)
}

func replacingPutCase(ctx context.Context, s sagas.SecretStore) error {
	return errors.Join(
		putSecret(ctx, ruleReplacingPut, s, jsontext.Value(`"first"`)),
		putSecret(ctx, ruleReplacingPut, s, jsontext.Value(`"second"`)),
		wantSecret(ctx, ruleReplacingPut, s, jsontext.Value(`"second"`)),
	)
}
