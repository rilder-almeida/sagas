package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sink struct {
	writer  io.Writer
	written func() string
}

type writerFactory func(t *testing.T) sink

func bufferWriter(t *testing.T) sink {
	t.Helper()

	var buf bytes.Buffer

	return sink{writer: &buf, written: buf.String}
}

func Test_run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newWriter  writerFactory
		wantOutput string
		wantErr    error
	}{
		{
			name:       "[Success] When: the writer accepts all writes ; Should: print both runs",
			newWriter:  bufferWriter,
			wantOutput: "trip-42: completed\ntrip-43: compensated, pending: []\n",
		},
		{
			name:      "[Fail] When: the writer fails ; Should: return the write error and write nothing",
			newWriter: failingWriter,
			wantErr:   syscall.EBADF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := tt.newWriter(t)

			err := run(out.writer)

			if tt.wantErr != nil {
				var pathErr *fs.PathError

				require.ErrorAs(t, err, &pathErr)
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantOutput, out.written())
		})
	}
}

func failingWriter(t *testing.T) sink {
	t.Helper()

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, root.Close()) })

	created, err := root.Create("out")
	require.NoError(t, err)
	require.NoError(t, created.Close())

	f, err := root.Open("out")
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, f.Close()) })

	return sink{writer: f, written: func() string {
		content, readErr := io.ReadAll(f)
		require.NoError(t, readErr)

		return string(content)
	}}
}

func Test_execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newStdout  writerFactory
		newStderr  writerFactory
		wantCode   int
		wantStderr bool
	}{
		{
			name:      "[Success] When: both writers accept writes ; Should: return 0 and write nothing to stderr",
			newStdout: bufferWriter,
			newStderr: bufferWriter,
		},
		{
			name:       "[Fail] When: stdout fails and stderr works ; Should: return 1 and report on stderr",
			newStdout:  failingWriter,
			newStderr:  bufferWriter,
			wantCode:   1,
			wantStderr: true,
		},
		{
			name:      "[Fail] When: stdout and stderr fail ; Should: return 2 and report nothing",
			newStdout: failingWriter,
			newStderr: failingWriter,
			wantCode:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr := tt.newStdout(t), tt.newStderr(t)

			assert.Equal(t, tt.wantCode, execute(stdout.writer, stderr.writer))
			assert.Equal(t, tt.wantStderr, stderr.written() != "")
		})
	}
}

func canceledContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	return ctx
}

func callEach(keys []string, call func(key string) (string, error)) ([]string, error) {
	ids := make([]string, 0, len(keys))

	var err error

	for _, key := range keys {
		var id string

		id, err = call(key)
		ids = append(ids, id)
	}

	return ids, err
}

func assertCalls(t *testing.T, ids []string, err error, wantDistinct int, wantErr error) {
	t.Helper()

	if wantErr != nil {
		require.ErrorIs(t, err, wantErr)
	} else {
		require.NoError(t, err)
	}

	assert.Equal(t, wantDistinct, distinct(ids))
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

func roomsLeft(t *testing.T, svc *hotels, limit int) int {
	t.Helper()

	for left := range limit + 1 {
		if _, err := svc.Book(t.Context(), fmt.Sprintf("probe-%d", left), "Lisbon", 1); err != nil {
			require.ErrorIs(t, err, errNoRooms)

			return left
		}
	}

	require.FailNow(t, "the hotel service still had rooms after the limit", "limit %d", limit)

	return limit
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

			ids, err := callEach(tt.keys, func(key string) (string, error) { return svc.Book(ctx, key, "Lisbon", 1) })

			assertCalls(t, ids, err, tt.wantDistinct, tt.wantErr)
			assert.Equal(t, tt.wantLeft, roomsLeft(t, svc, tt.free))
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

			ids, err := callEach(tt.keys, func(key string) (string, error) { return svc.Charge(ctx, key, tt.amount) })

			assertCalls(t, ids, err, tt.wantDistinct, tt.wantErr)
		})
	}
}

func Test_mailer_Send(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		address      string
		keys         []string
		canceled     bool
		wantDistinct int
		wantErr      error
	}{
		{
			name:         "[Success] When: one key sends three times ; Should: return one ID",
			address:      "ana@example.com",
			keys:         []string{"k1", "k1", "k1"},
			wantDistinct: 1,
		},
		{
			name:         "[Success] When: two keys send ; Should: return two IDs",
			address:      "ana@example.com",
			keys:         []string{"k1", "k2"},
			wantDistinct: 2,
		},
		{
			name:    "[Fail] When: the address is empty ; Should: return errNoAddress",
			keys:    []string{"k1"},
			wantErr: errNoAddress,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error",
			address:  "ana@example.com",
			keys:     []string{"k1"},
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

			ids, err := callEach(tt.keys, func(key string) (string, error) { return svc.Send(ctx, key, tt.address) })

			assertCalls(t, ids, err, tt.wantDistinct, tt.wantErr)
		})
	}
}

func Test_airline_Book(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		keys         []string
		canceled     bool
		wantDistinct int
		wantErr      error
	}{
		{
			name:         "[Success] When: the same key books twice ; Should: return one ID",
			keys:         []string{"k1", "k1"},
			wantDistinct: 1,
		},
		{
			name:         "[Success] When: two keys book ; Should: return two IDs",
			keys:         []string{"k1", "k2"},
			wantDistinct: 2,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error and book nothing",
			keys:     []string{"k1"},
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newAirline()

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			ids, err := callEach(tt.keys, func(key string) (string, error) { return svc.Book(ctx, key, "GRU-LIS") })

			assertCalls(t, ids, err, tt.wantDistinct, tt.wantErr)
		})
	}
}

func Test_hotels_Cancel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		free     int
		cancel   func(booked string) []string
		canceled bool
		wantLeft int
		wantErr  error
	}{
		{
			name:     "[Success] When: the booking is canceled twice ; Should: give back one room only",
			free:     2,
			cancel:   func(booked string) []string { return []string{booked, booked} },
			wantLeft: 2,
		},
		{
			name:     "[Success] When: an unknown ID is canceled ; Should: give back no room",
			free:     2,
			cancel:   func(string) []string { return []string{"HT-none"} },
			wantLeft: 1,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error and keep the room taken",
			free:     2,
			cancel:   func(booked string) []string { return []string{booked} },
			canceled: true,
			wantLeft: 1,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newHotels(map[string]int{"Lisbon": tt.free})

			booked, err := svc.Book(t.Context(), "k1", "Lisbon", 1)
			require.NoError(t, err)

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			for _, id := range tt.cancel(booked) {
				err = svc.Cancel(ctx, id)
			}

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantLeft, roomsLeft(t, svc, tt.free))
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
		{name: "[Success] When: the flight is not canceled ; Should: stay booked", wantBooked: true},
		{name: "[Success] When: the flight is canceled twice ; Should: end not booked", cancels: 2},
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
