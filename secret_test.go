package sagas

import (
	"context"
	"encoding/json/jsontext"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	secretExecution = "exec-1"
	secretWriter    = "w1"
	cardField       = "Card"
	cardJSON        = `"4111"`
)

type secretPut struct {
	key   SecretKey
	value string
}

func cardKey(seq int) SecretKey {
	return SecretKey{Execution: secretExecution, Writer: secretWriter, Seq: seq, Field: cardField}
}

func cardPut(seq int, value string) secretPut {
	return secretPut{key: cardKey(seq), value: value}
}

func putAll(t *testing.T, s *MemorySecretStore, puts []secretPut) {
	t.Helper()

	for _, p := range puts {
		require.NoError(t, s.Put(t.Context(), p.key, jsontext.Value(p.value)))
	}
}

func doneContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	return ctx
}

func Test_NewMemorySecretStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		puts    []secretPut
		want    jsontext.Value
		wantErr error
	}{
		{
			name:    "[Fail] When: nothing was put ; Should: return ErrNoSecret",
			wantErr: ErrNoSecret,
		},
		{
			name: "[Success] When: a value was put ; Should: hold it",
			puts: []secretPut{cardPut(1, cardJSON)},
			want: jsontext.Value(cardJSON),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := NewMemorySecretStore()
			putAll(t, s, tt.puts)

			got, err := s.Get(t.Context(), cardKey(1))
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_MemorySecretStore_Put(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		zero     bool
		earlier  []secretPut
		put      secretPut
		done     bool
		scribble bool
		wantErr  error
		want     jsontext.Value
		wantGet  error
	}{
		{
			name: "[Success] When: a value is put ; Should: hold it for its key",
			put:  cardPut(1, cardJSON),
			want: jsontext.Value(cardJSON),
		},
		{
			name: "[Success] When: the store is a zero value ; Should: hold the value",
			zero: true,
			put:  cardPut(1, cardJSON),
			want: jsontext.Value(cardJSON),
		},
		{
			name:     "[Success] When: the caller changes its value after the put ; Should: hold a copy",
			put:      cardPut(1, cardJSON),
			scribble: true,
			want:     jsontext.Value(cardJSON),
		},
		{
			name:    "[Success] When: a second put on the same key ; Should: replace the value",
			earlier: []secretPut{cardPut(1, `"5500"`)},
			put:     cardPut(1, cardJSON),
			want:    jsontext.Value(cardJSON),
		},
		{
			name:    "[Fail] When: the context is done ; Should: return its error and store nothing",
			put:     cardPut(1, cardJSON),
			done:    true,
			wantErr: context.Canceled,
			wantGet: ErrNoSecret,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := NewMemorySecretStore()
			if tt.zero {
				s = &MemorySecretStore{}
			}

			putAll(t, s, tt.earlier)

			ctx := t.Context()
			if tt.done {
				ctx = doneContext(t)
			}

			value := jsontext.Value(tt.put.value)
			require.ErrorIs(t, s.Put(ctx, tt.put.key, value), tt.wantErr)

			if tt.scribble {
				value[1] = 'x'
			}

			got, err := s.Get(t.Context(), tt.put.key)
			require.ErrorIs(t, err, tt.wantGet)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_MemorySecretStore_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		zero      bool
		puts      []secretPut
		key       SecretKey
		done      bool
		scribble  bool
		want      jsontext.Value
		wantErr   error
		wantInMsg string
	}{
		{
			name: "[Success] When: the key holds a value ; Should: return it",
			puts: []secretPut{cardPut(1, cardJSON), cardPut(2, `"5500"`)},
			key:  cardKey(2),
			want: jsontext.Value(`"5500"`),
		},
		{
			name:     "[Success] When: the caller changes the returned value ; Should: keep the stored one",
			puts:     []secretPut{cardPut(1, cardJSON)},
			key:      cardKey(1),
			scribble: true,
			want:     jsontext.Value(cardJSON),
		},
		{
			name:      "[Fail] When: the seq holds no value ; Should: return ErrNoSecret naming the key",
			puts:      []secretPut{cardPut(1, cardJSON)},
			key:       cardKey(2),
			wantErr:   ErrNoSecret,
			wantInMsg: `sagas: no secret: execution "exec-1" writer "w1" seq 2 field "Card"`,
		},
		{
			name:    "[Fail] When: another field holds the value ; Should: return ErrNoSecret",
			puts:    []secretPut{cardPut(1, cardJSON)},
			key:     SecretKey{Execution: secretExecution, Writer: secretWriter, Seq: 1, Field: "Pin"},
			wantErr: ErrNoSecret,
		},
		{
			name:    "[Fail] When: another execution holds the value ; Should: return ErrNoSecret",
			puts:    []secretPut{cardPut(1, cardJSON)},
			key:     SecretKey{Execution: "exec-2", Writer: secretWriter, Seq: 1, Field: cardField},
			wantErr: ErrNoSecret,
		},
		{
			name:    "[Fail] When: another writer of the execution holds the value ; Should: return ErrNoSecret",
			puts:    []secretPut{cardPut(1, cardJSON)},
			key:     SecretKey{Execution: secretExecution, Writer: "w2", Seq: 1, Field: cardField},
			wantErr: ErrNoSecret,
		},
		{
			name:    "[Fail] When: the store is a zero value ; Should: return ErrNoSecret",
			zero:    true,
			key:     cardKey(1),
			wantErr: ErrNoSecret,
		},
		{
			name:    "[Fail] When: the context is done ; Should: return its error",
			puts:    []secretPut{cardPut(1, cardJSON)},
			key:     cardKey(1),
			done:    true,
			wantErr: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := NewMemorySecretStore()
			if tt.zero {
				s = &MemorySecretStore{}
			}

			putAll(t, s, tt.puts)

			ctx := t.Context()
			if tt.done {
				ctx = doneContext(t)
			}

			got, err := s.Get(ctx, tt.key)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.want, got)

			if tt.wantInMsg != "" {
				require.EqualError(t, err, tt.wantInMsg)
			}

			if tt.scribble {
				got[1] = 'x'

				again, againErr := s.Get(t.Context(), tt.key)
				require.NoError(t, againErr)
				assert.Equal(t, tt.want, again)
			}
		})
	}
}
