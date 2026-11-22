package main

import (
	"bytes"
	"context"
	"fmt"
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
		name          string
		failingWriter bool
		wantOutput    string
		wantWriteOp   string
	}{
		{
			name:       "[Success] When: the writer accepts all writes ; Should: print both runs",
			wantOutput: "trip-42: completed\ntrip-43: compensated, pending: []\n",
		},
		{
			name:          "[Fail] When: the writer fails ; Should: return the write error",
			failingWriter: true,
			wantWriteOp:   "write",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var (
				buf bytes.Buffer
				w   io.Writer = &buf
			)

			if tt.failingWriter {
				w = readOnlyFile(t)
			}

			err := run(w)

			if tt.wantWriteOp == "" {
				require.NoError(t, err)
			} else {
				var pathErr *fs.PathError

				require.ErrorAs(t, err, &pathErr)
				assert.Equal(t, tt.wantWriteOp, pathErr.Op)
			}

			assert.Equal(t, tt.wantOutput, buf.String())
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

func distinct(ids []string) int {
	seen := map[string]bool{}

	for _, id := range ids {
		if id != "" {
			seen[id] = true
		}
	}

	return len(seen)
}

func roomsLeft(t *testing.T, svc *hotels) int {
	t.Helper()

	left := 0

	for ; ; left++ {
		_, err := svc.Book(t.Context(), fmt.Sprintf("probe-%d", left), "Lisbon", 1)
		if err != nil {
			require.ErrorIs(t, err, errNoRooms)

			return left
		}
	}
}

func Test_hotels_Book(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		free         int
		keys         []string
		canceled     bool
		wantDistinct int
		wantLeft     int
		wantErr      error
	}{
		{
			name:         "[Success] When: the same key books twice ; Should: return one ID and take one room",
			free:         2,
			keys:         []string{"k1", "k1"},
			wantDistinct: 1,
			wantLeft:     1,
		},
		{
			name:         "[Success] When: two keys book ; Should: return two IDs and take two rooms",
			free:         2,
			keys:         []string{"k1", "k2"},
			wantDistinct: 2,
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
			wantLeft: 1,
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
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantDistinct, distinct(ids))
			assert.Equal(t, tt.wantLeft, roomsLeft(t, svc))
		})
	}
}

func Test_payments_Charge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		amount       int
		keys         []string
		canceled     bool
		wantDistinct int
		wantErr      error
	}{
		{
			name:         "[Success] When: the same key charges twice ; Should: return one ID",
			amount:       cardLimit,
			keys:         []string{"k1", "k1"},
			wantDistinct: 1,
		},
		{
			name:         "[Success] When: two keys charge ; Should: return two IDs",
			amount:       cardLimit,
			keys:         []string{"k1", "k2"},
			wantDistinct: 2,
		},
		{
			name:    "[Fail] When: the amount is above the limit ; Should: return errOverLimit",
			amount:  cardLimit + 1,
			keys:    []string{"k1"},
			wantErr: errOverLimit,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error",
			amount:   1,
			keys:     []string{"k1"},
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

			ids := make([]string, 0, len(tt.keys))

			var err error

			for _, key := range tt.keys {
				var id string

				id, err = svc.Charge(ctx, key, tt.amount)
				ids = append(ids, id)
			}

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantDistinct, distinct(ids))
		})
	}
}

func Test_mailer_Send(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		address      string
		sends        int
		canceled     bool
		wantDistinct int
		wantErr      error
	}{
		{
			name:         "[Success] When: one key sends three times ; Should: return one ID",
			address:      "ana@example.com",
			sends:        3,
			wantDistinct: 1,
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

			ids := make([]string, 0, tt.sends)

			var err error

			for range tt.sends {
				var id string

				id, err = svc.Send(ctx, "k1", tt.address)
				ids = append(ids, id)
			}

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantDistinct, distinct(ids))
		})
	}
}

func Test_airline_Cancel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cancels    int
		canceled   bool
		wantBooked bool
		wantErr    error
	}{
		{name: "[Success] When: the flight is not cancelled ; Should: stay booked", wantBooked: true},
		{name: "[Success] When: the flight is cancelled twice ; Should: end not booked", cancels: 2},
		{
			name:       "[Fail] When: the context is done ; Should: return its error and keep the flight",
			cancels:    1,
			canceled:   true,
			wantBooked: true,
			wantErr:    context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newAirline()

			id, err := svc.Book(t.Context(), "k1", "GRU-LIS")
			require.NoError(t, err)

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			for range tt.cancels {
				err = svc.Cancel(ctx, id)
			}

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantBooked, svc.Booked(id))
		})
	}
}
