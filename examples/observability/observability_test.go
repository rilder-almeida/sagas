package main

import (
	"bytes"
	"context"
	"errors"
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

var errRefused = errors.New("refused")

func Test_run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newWriter  writerFactory
		wantOutput string
		wantErr    error
	}{
		{
			name:      "[Success] When: the writer accepts all writes ; Should: log each attempt and the retry",
			newWriter: bufferWriter,
			wantOutput: `{"level":"INFO","msg":"sagas: attempt","idempotency_key":"order/order-1/reserve",` +
				`"saga":"order","execution":"order-1","step":"reserve","phase":"execute","attempt":1}` + "\n" +
				`{"level":"WARN","msg":"sagas: attempt","idempotency_key":"order/order-1/charge",` +
				`"saga":"order","execution":"order-1","step":"charge","phase":"execute","attempt":1,` +
				`"error":"provider busy"}` + "\n" +
				`{"level":"INFO","msg":"sagas: attempt","idempotency_key":"order/order-1/charge",` +
				`"saga":"order","execution":"order-1","step":"charge","phase":"execute","attempt":2}` + "\n",
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

func Test_keyLog_Wrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		info     sagas.CallInfo
		callErr  error
		wantLine string
	}{
		{
			name: "[Success] When: the call succeeds ; Should: log at info with the idempotency key",
			info: sagas.CallInfo{Saga: "s", Execution: "e-1", Step: "a", Phase: sagas.PhaseExecute, Attempt: 1},
			wantLine: `{"level":"INFO","msg":"sagas: attempt","idempotency_key":"s/e-1/a",` +
				`"saga":"s","execution":"e-1","step":"a","phase":"execute","attempt":1}` + "\n",
		},
		{
			name:    "[Fail] When: the call fails ; Should: log at warn with key and error, and return it",
			info:    sagas.CallInfo{Saga: "s", Execution: "e-2", Step: "b", Phase: sagas.PhaseCompensate, Attempt: 3},
			callErr: errRefused,
			wantLine: `{"level":"WARN","msg":"sagas: attempt","idempotency_key":"s/e-2/b",` +
				`"saga":"s","execution":"e-2","step":"b","phase":"compensate","attempt":3,"error":"refused"}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			mw := keyLog{logger: newLogger(&out)}

			err := mw.Wrap(t.Context(), tt.info, func(context.Context) error { return tt.callErr })

			require.ErrorIs(t, err, tt.callErr)
			assert.Equal(t, tt.wantLine, out.String())
		})
	}
}
