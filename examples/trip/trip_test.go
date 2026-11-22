package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"github.com/rilder-almeida/sagas"
	"github.com/rilder-almeida/sagas/sagastest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errLeftBehind   = errors.New("bookings left behind")
	errNoLateAction = errors.New("no late action for step")
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

func checkBookings(svc *services, got *trip) error {
	want := 0
	if got.ChargeID != "" {
		want = 1
	}

	held, taken := svc.airline.Held(), lisbonRooms-svc.hotels.Free("Lisbon")
	if held != want || taken != want {
		return fmt.Errorf("%w: flights held %d, rooms taken %d, want %d of each", errLeftBehind, held, taken, want)
	}

	return nil
}

func bookLate(ctx context.Context, svc *services, t *trip, step, key string) error {
	switch step {
	case "flight":
		_, err := svc.airline.Book(ctx, key, t.Route)

		return err
	case "hotel":
		_, err := svc.hotels.Book(ctx, key, t.City, t.Nights)

		return err
	default:
		return fmt.Errorf("%w: %q", errNoLateAction, step)
	}
}

func tripSetup(t *testing.T) sagastest.Setup[trip] {
	t.Helper()

	return func(mw ...sagas.Middleware) sagastest.Case[trip] {
		svc := newServices()

		saga, err := sagas.New[trip]("trip", svc.flow, sagas.WithMiddleware(mw...))
		require.NoError(t, err)

		state := &newTrips()[0]

		return sagastest.Case[trip]{
			Saga:  saga,
			State: state,
			Flow:  svc.flow,
			Check: func(got *trip) error { return checkBookings(svc, got) },
			Late:  func(ctx context.Context, step, key string) error { return bookLate(ctx, svc, state, step, key) },
		}
	}
}

func Test_newServices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		prove func(testing.TB, sagastest.Setup[trip])
	}{
		{
			name:  "[Success] When: Compensations runs on the trip ; Should: pass three cases per compensation",
			prove: sagastest.Compensations[trip],
		},
		{
			name:  "[Success] When: Pivot runs on the trip ; Should: charge once and repeat the email safely",
			prove: sagastest.Pivot[trip],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.prove(t, tripSetup(t))
		})
	}
}
