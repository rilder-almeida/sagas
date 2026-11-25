package main

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"github.com/rilder-almeida/sagas"
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

func Test_run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newWriter  writerFactory
		wantOutput string
		wantErr    error
	}{
		{
			name:      "[Success] When: the writer accepts all writes ; Should: print the result and each pending step",
			newWriter: bufferWriter,
			wantOutput: "order-1: incomplete, pending: [ship notify]\n" +
				"ship: last event step-failed, error: carrier is closed\n" +
				"notify: no event\n",
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

func Test_inspectRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		carrier    string
		runs       int
		wantOutput string
		wantErr    error
	}{
		{
			name:       "[Success] When: every step succeeded ; Should: say the run completed and list nothing",
			carrier:    carrierOpen,
			runs:       1,
			wantOutput: "order-1: completed\n",
		},
		{
			name:    "[Success] When: a step past the pivot failed ; Should: list each pending step and its last event",
			carrier: carrierClosed,
			runs:    1,
			wantOutput: "order-1: incomplete, pending: [ship notify]\n" +
				"ship: last event step-failed, error: carrier is closed\n" +
				"notify: no event\n",
		},
		{
			name:    "[Fail] When: the execution already exists ; Should: return ErrExecutionExists",
			carrier: carrierOpen,
			runs:    2,
			wantErr: sagas.ErrExecutionExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			journal := sagas.NewMemoryJournal()

			saga, err := newSaga(journal)
			require.NoError(t, err)

			var runErr error

			for range tt.runs {
				runErr = saga.Run(ctx, &order{ID: "order-1", Carrier: tt.carrier})
			}

			var out bytes.Buffer

			err = inspectRun(ctx, &out, journal, "order-1", runErr)

			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantOutput, out.String())
		})
	}
}

func Test_currentEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		seed       []sagas.Event
		wantEvents int
		wantErr    error
	}{
		{
			name:       "[Success] When: the journal holds two events ; Should: return both",
			seed:       []sagas.Event{eventAt(1, sagas.EventSagaStarted), eventAt(2, sagas.EventSagaFinished)},
			wantEvents: 2,
		},
		{
			name:    "[Fail] When: the journal holds no event for the execution ; Should: return ErrNoEvents",
			wantErr: sagas.ErrNoEvents,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			journal := sagas.NewMemoryJournal()

			for i := range tt.seed {
				require.NoError(t, journal.Append(t.Context(), &tt.seed[i]))
			}

			events, err := currentEvents(t.Context(), journal, "order-1")

			require.ErrorIs(t, err, tt.wantErr)
			assert.Len(t, events, tt.wantEvents)
		})
	}
}

func eventAt(seq int, kind sagas.EventKind) sagas.Event {
	return sagas.Event{Execution: "order-1", Seq: seq, Version: sagas.EventVersion, Kind: kind}
}

func Test_checkVersions(t *testing.T) {
	t.Parallel()

	current := eventAt(1, sagas.EventSagaStarted)
	future := sagas.Event{Version: "v0.3.0", Kind: sagas.EventSagaStarted}

	tests := []struct {
		name    string
		events  []sagas.Event
		wantErr error
	}{
		{
			name:   "[Success] When: every event has the current Version ; Should: return nil",
			events: []sagas.Event{current, current},
		},
		{
			name:   "[Success] When: there are no events ; Should: return nil",
			events: nil,
		},
		{
			name:    "[Fail] When: one event has a newer Version ; Should: return errUnknownVersion",
			events:  []sagas.Event{current, future},
			wantErr: errUnknownVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, checkVersions("order-1", tt.events), tt.wantErr)
		})
	}
}

const shipStep = "ship"

func Test_lastEventOf(t *testing.T) {
	t.Parallel()

	failed := sagas.Event{Step: shipStep, Kind: sagas.EventStepFailed, Err: sagas.EventError{Message: "no truck"}}
	started := sagas.Event{Step: shipStep, Kind: sagas.EventStepStarted}
	other := sagas.Event{Step: "notify", Kind: sagas.EventStepStarted}

	tests := []struct {
		name   string
		events []sagas.Event
		step   string
		want   string
	}{
		{
			name:   "[Success] When: the last event of the step has a message ; Should: show the kind and the message",
			events: []sagas.Event{started, other, failed},
			step:   shipStep,
			want:   "last event step-failed, error: no truck",
		},
		{
			name:   "[Success] When: the last event of the step has no error ; Should: show the kind and none",
			events: []sagas.Event{failed, started},
			step:   shipStep,
			want:   "last event step-started, error: none",
		},
		{
			name:   "[Success] When: another step is asked for ; Should: show the last event of that step",
			events: []sagas.Event{failed, other},
			step:   "notify",
			want:   "last event step-started, error: none",
		},
		{
			name:   "[Success] When: the step has no event ; Should: say no event",
			events: []sagas.Event{other},
			step:   shipStep,
			want:   "no event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, lastEventOf(tt.events, tt.step))
		})
	}
}
