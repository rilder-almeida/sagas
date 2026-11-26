package main

import (
	"bytes"
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
			name:      "[Success] When: the writer accepts all writes ; Should: print the three runs",
			newWriter: bufferWriter,
			wantOutput: "ord-1: completed\n" +
				"ord-2: compensated, pending: [], undone: [reserve]\n" +
				"ord-3: refused: sagas: invalid step: steps \"note-1\" and \"note-2\": both produce main.note " +
				"and may run at the same time\n",
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

func Test_newSaga(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		amount       int
		wantReport   string
		wantHeld     bool
		wantRisk     string
		wantAttempts []string
	}{
		{
			name: "[Success] When: the order passes the fraud check ; " +
				"Should: complete and keep the reservation",
			amount:       fraudLimit,
			wantReport:   "ord-9: completed\n",
			wantHeld:     true,
			wantRisk:     "low",
			wantAttempts: []string{"reserve execute", "fraud-check execute", "charge pivot", "ship execute"},
		},
		{
			name:         "[Fail] When: the fraud check flags the order ; Should: release the reservation, no charge",
			amount:       fraudLimit + 1,
			wantReport:   "ord-9: compensated, pending: [], undone: [reserve]\n",
			wantAttempts: []string{"reserve execute", "fraud-check execute", "reserve compensate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newServices()
			rec := sagastest.NewRecorder()

			saga, err := svc.build(nil, rec)
			require.NoError(t, err)

			o := order{ID: "ord-9", Item: "lamp", Amount: tt.amount}
			runErr := saga.Run(t.Context(), &o)

			var out bytes.Buffer

			require.NoError(t, report(&out, &o, svc.undone.take(), runErr))
			assert.Equal(t, tt.wantReport, out.String())

			key := sagas.CallInfo{Saga: "order", Execution: o.ID, Step: "reserve"}.IdempotencyKey()
			id, putErr := svc.warehouse.put(t.Context(), key)

			require.NoError(t, putErr)
			assert.Equal(t, tt.wantHeld, id == "SKU-1")
			assert.Equal(t, risk(tt.wantRisk), o.Risk)

			recorded := rec.Attempts()
			attempts := make([]string, 0, len(recorded))

			for _, attempt := range recorded {
				attempts = append(attempts, attempt.Step+" "+string(attempt.Phase))
			}

			assert.Equal(t, tt.wantAttempts, attempts)
		})
	}
}
