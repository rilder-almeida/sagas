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
			name:       "[Success] When: the writer accepts all writes ; Should: print both runs",
			newWriter:  bufferWriter,
			wantOutput: "acct-1: completed\nacct-2: compensated, pending: []\n",
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
		name           string
		id             string
		plan           string
		wantRegistered bool
	}{
		{
			name:           "[Success] When: the sign-up completes ; Should: keep the registered account",
			id:             "acct-1",
			plan:           "basic",
			wantRegistered: true,
		},
		{
			name: "[Fail] When: provision fails ; Should: undo the registration",
			id:   "acct-2",
			plan: "dedicated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			accounts := newStore("ACC")

			saga, err := newSaga(accounts)
			require.NoError(t, err)

			a := account{ID: tt.id, Email: "ana@example.com", Plan: tt.plan}
			runErr := saga.Run(t.Context(), &a)

			key := sagas.CallInfo{Saga: "signup", Execution: a.ID, Step: "register"}.IdempotencyKey()
			id, putErr := accounts.put(t.Context(), key, a.Email)

			require.NoError(t, putErr)
			assert.Equal(t, tt.wantRegistered, id == a.AccountID)

			if tt.wantRegistered {
				require.NoError(t, runErr)
			} else {
				require.Error(t, runErr)
			}
		})
	}
}

func Test_store_removeIfPresent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		removeKey   string
		storedKey   string
		wantRemoved bool
	}{
		{
			name:        "[Success] When: the key was stored ; Should: remove it",
			removeKey:   "k3",
			storedKey:   "k3",
			wantRemoved: true,
		},
		{
			name:      "[Success] When: the key never applied ; Should: succeed and keep the other keys",
			removeKey: "k2",
			storedKey: "k1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newStore("ACC")
			stored, err := s.put(t.Context(), tt.storedKey, "v")
			require.NoError(t, err)

			require.NoError(t, s.removeIfPresent(t.Context(), tt.removeKey))

			again, err := s.put(t.Context(), tt.storedKey, "v")
			require.NoError(t, err)
			assert.Equal(t, !tt.wantRemoved, again == stored)
		})
	}
}
