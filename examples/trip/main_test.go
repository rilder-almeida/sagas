package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		readOnly  bool
		want      string
		wantWrite bool
	}{
		{
			name: "[Success] When: the writer accepts all writes ; Should: print both runs",
			want: "trip-42: completed\ntrip-43: compensated, pending: []\n",
		},
		{
			name:      "[Fail] When: the writer fails ; Should: return the write error",
			readOnly:  true,
			wantWrite: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var (
				buf bytes.Buffer
				w   io.Writer = &buf
			)

			if tt.readOnly {
				w = readOnlyFile(t)
			}

			err := run(w)

			if tt.wantWrite {
				var pathErr *fs.PathError

				require.ErrorAs(t, err, &pathErr)
				assert.Equal(t, "write", pathErr.Op)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, buf.String())
		})
	}
}

func readOnlyFile(t *testing.T) *os.File {
	t.Helper()

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, root.Close()) })

	w, err := root.Create("out")
	require.NoError(t, err)
	require.NoError(t, w.Close())

	f, err := root.Open("out")
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, f.Close()) })

	return f
}

func Test_execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		stdoutReadOnly bool
		stderrReadOnly bool
		wantCode       int
		wantStderr     bool
	}{
		{
			name:     "[Success] When: both writers accept writes ; Should: return 0",
			wantCode: 0,
		},
		{
			name:           "[Fail] When: stdout fails and stderr works ; Should: return 1 and report on stderr",
			stdoutReadOnly: true,
			wantCode:       1,
			wantStderr:     true,
		},
		{
			name:           "[Fail] When: stdout and stderr fail ; Should: return 2",
			stdoutReadOnly: true,
			stderrReadOnly: true,
			wantCode:       2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			var out, errw io.Writer = &stdout, &stderr

			if tt.stdoutReadOnly {
				out = readOnlyFile(t)
			}

			if tt.stderrReadOnly {
				errw = readOnlyFile(t)
			}

			assert.Equal(t, tt.wantCode, execute(out, errw))
			assert.Equal(t, tt.wantStderr, stderr.Len() > 0)
		})
	}
}

func canceledContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	return ctx
}

func Test_hotels_Book(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		free     int
		keys     []string
		canceled bool
		wantSame bool
		wantFree int
		wantErr  error
	}{
		{
			name:     "[Success] When: the same key books twice ; Should: return one ID and take one room",
			free:     2,
			keys:     []string{"k1", "k1"},
			wantSame: true,
			wantFree: 1,
		},
		{
			name:     "[Success] When: two keys book ; Should: return two IDs and take two rooms",
			free:     2,
			keys:     []string{"k1", "k2"},
			wantFree: 0,
		},
		{
			name:    "[Fail] When: the city has no rooms ; Should: return errNoRooms",
			keys:    []string{"k1"},
			wantErr: errNoRooms,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error and take no room",
			free:     1,
			keys:     []string{"k1"},
			canceled: true,
			wantFree: 1,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newHotels(map[string]int{"Lisbon": tt.free})

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			ids := make([]string, 0, len(tt.keys))

			var err error

			for _, key := range tt.keys {
				var id string

				id, err = svc.Book(ctx, key, "Lisbon", 1)
				ids = append(ids, id)
			}

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantSame, ids[0] == ids[1])
			assert.Equal(t, tt.wantFree, svc.free["Lisbon"])
		})
	}
}

func Test_payments_Charge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		amount   int
		canceled bool
		wantErr  error
	}{
		{name: "[Success] When: the amount is within the limit ; Should: charge it", amount: cardLimit},
		{
			name:    "[Fail] When: the amount is above the limit ; Should: return errOverLimit",
			amount:  cardLimit + 1,
			wantErr: errOverLimit,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error",
			amount:   1,
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newPayments()

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			first, err := svc.Charge(ctx, "k1", tt.amount)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			again, err := svc.Charge(ctx, "k1", tt.amount)
			require.NoError(t, err)
			assert.Equal(t, first, again)
			assert.Len(t, svc.byKey, 1)
		})
	}
}

func Test_mailer_Send(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		address  string
		sends    int
		canceled bool
		wantMail int
		wantErr  error
	}{
		{
			name:     "[Success] When: one key sends three times ; Should: record one mail",
			address:  "ana@example.com",
			sends:    3,
			wantMail: 1,
		},
		{
			name:    "[Fail] When: the address is empty ; Should: return errNoAddress",
			sends:   1,
			wantErr: errNoAddress,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error",
			address:  "ana@example.com",
			sends:    1,
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newMailer()

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			var err error

			for range tt.sends {
				_, err = svc.Send(ctx, "k1", tt.address)
			}

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Len(t, svc.byKey, tt.wantMail)
		})
	}
}
