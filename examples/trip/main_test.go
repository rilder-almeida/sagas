package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/rilder-almeida/sagas"
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

type stepTest struct {
	name     string
	canceled bool
	start    trip
	want     trip
	wantErr  error
}

func canceledContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	return ctx
}

func (tt *stepTest) check(t *testing.T, call func(context.Context, *trip) error) {
	t.Helper()

	ctx := t.Context()
	if tt.canceled {
		ctx = canceledContext(t)
	}

	st := tt.start
	err := call(ctx, &st)

	if tt.wantErr != nil {
		require.ErrorIs(t, err, tt.wantErr)
	} else {
		require.NoError(t, err)
	}

	assert.Equal(t, tt.want, st)
}

func Test_flight_Execute(t *testing.T) {
	t.Parallel()

	tests := []stepTest{
		{
			name: "[Success] When: the context is live ; Should: write the key, empty outside a run",
			want: trip{Flight: sagas.IdempotencyKey(t.Context())},
		},
		{
			name:     "[Fail] When: the context is canceled ; Should: return context.Canceled and leave Flight",
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	call := (&flight{}).Execute

	for i := range tests {
		tt := &tests[i]

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(t, call)
		})
	}
}

func Test_flight_Compensate(t *testing.T) {
	t.Parallel()

	tests := []stepTest{
		{
			name:  "[Success] When: the context is live ; Should: clear Flight",
			start: trip{Flight: "key"},
		},
		{
			name:     "[Fail] When: the context is canceled ; Should: return context.Canceled and leave Flight",
			canceled: true,
			start:    trip{Flight: "key"},
			want:     trip{Flight: "key"},
			wantErr:  context.Canceled,
		},
	}

	call := (&flight{}).Compensate

	for i := range tests {
		tt := &tests[i]

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(t, call)
		})
	}
}

func Test_hotel_Execute(t *testing.T) {
	t.Parallel()

	tests := []stepTest{
		{
			name:  "[Success] When: a room is left ; Should: book it",
			start: trip{RoomsLeft: 1},
			want:  trip{RoomsLeft: 1, Hotel: booked},
		},
		{
			name:    "[Fail] When: no room is left ; Should: return a permanent errNoRooms and leave Hotel",
			wantErr: errNoRooms,
		},
		{
			name:     "[Fail] When: the context is canceled ; Should: return context.Canceled and leave Hotel",
			canceled: true,
			start:    trip{RoomsLeft: 1},
			want:     trip{RoomsLeft: 1},
			wantErr:  context.Canceled,
		},
	}

	call := (&hotel{}).Execute

	for i := range tests {
		tt := &tests[i]

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(t, call)
		})
	}
}

func Test_hotel_Compensate(t *testing.T) {
	t.Parallel()

	tests := []stepTest{
		{
			name:  "[Success] When: the context is live ; Should: clear Hotel",
			start: trip{Hotel: booked},
		},
		{
			name:     "[Fail] When: the context is canceled ; Should: return context.Canceled and leave Hotel",
			canceled: true,
			start:    trip{Hotel: booked},
			want:     trip{Hotel: booked},
			wantErr:  context.Canceled,
		},
	}

	call := (&hotel{}).Compensate

	for i := range tests {
		tt := &tests[i]

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(t, call)
		})
	}
}

func Test_charge_Pivot(t *testing.T) {
	t.Parallel()

	tests := []stepTest{
		{
			name: "[Success] When: the context is live ; Should: write Charge",
			want: trip{Charge: "charged"},
		},
		{
			name:     "[Fail] When: the context is canceled ; Should: return context.Canceled and leave Charge",
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	call := (&charge{}).Pivot

	for i := range tests {
		tt := &tests[i]

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(t, call)
		})
	}
}

func Test_email_Execute(t *testing.T) {
	t.Parallel()

	tests := []stepTest{
		{
			name: "[Success] When: the context is live ; Should: write Email",
			want: trip{Email: "sent"},
		},
		{
			name:     "[Fail] When: the context is canceled ; Should: return context.Canceled and leave Email",
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	call := (&email{}).Execute

	for i := range tests {
		tt := &tests[i]

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(t, call)
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

func Test_trip_ExecutionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
	}{
		{name: "[Success] When: the trip has an ID ; Should: return it", id: "trip-42"},
		{name: "[Success] When: the trip has no ID ; Should: return an empty string", id: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.id, (&trip{ID: tt.id}).ExecutionID())
		})
	}
}
