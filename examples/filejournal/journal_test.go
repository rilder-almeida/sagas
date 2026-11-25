package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rilder-almeida/sagas"
	"github.com/rilder-almeida/sagas/sagastest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const runID = "run-1"

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
			name:      "[Success] When: the writer accepts all writes ; Should: print one line per event kind",
			newWriter: bufferWriter,
			wantOutput: "saga-finished: 1\n" +
				"saga-started: 1\n" +
				"step-started: 3\n" +
				"step-succeeded: 3\n",
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

func newTestJournal(tb testing.TB, dir string) *fileJournal {
	tb.Helper()

	j, err := openFileJournal(dir)
	require.NoError(tb, err)

	tb.Cleanup(func() { assert.NoError(tb, j.Close()) })

	return j
}

func testEvent(seq int, step string) *sagas.Event {
	return &sagas.Event{
		Saga: "saga", Execution: runID, Writer: "writer", Seq: seq, Version: sagas.EventVersion,
		Kind: sagas.EventStepStarted, Step: step, Attempt: 1,
		Time: time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC),
	}
}

func Test_fileJournal_Append_contract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "[Success] When: each journal has an empty directory ; Should: keep every rule of sagas.Journal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sagastest.Journal(t, func() sagas.Journal { return newTestJournal(t, t.TempDir()) })
		})
	}
}

func seedFile(t *testing.T, dir, execution string, events []sagas.Event, tail string) {
	t.Helper()

	var content []byte

	for i := range events {
		line, err := json.Marshal(events[i])
		require.NoError(t, err)

		content = append(content, line...)
		content = append(content, '\n')
	}

	content = append(content, tail...)

	require.NoError(t, os.WriteFile(filepath.Join(dir, journalFile(execution)), content, 0o600))
}

func Test_fileJournal_Events(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		seed       []sagas.Event
		tail       string
		read       string
		wantEvents []sagas.Event
		wantErr    error
	}{
		{
			name:       "[Success] When: the file holds two whole lines ; Should: return both events in order",
			seed:       []sagas.Event{*testEvent(1, "a"), *testEvent(2, "b")},
			read:       runID,
			wantEvents: []sagas.Event{*testEvent(1, "a"), *testEvent(2, "b")},
		},
		{
			name:    "[Fail] When: the execution has no file ; Should: return ErrNoEvents",
			seed:    []sagas.Event{*testEvent(1, "a")},
			read:    "other-run",
			wantErr: sagas.ErrNoEvents,
		},
		{
			name: "[Fail] When: a line has another Version ; Should: return errUnknownVersion and no events",
			seed: []sagas.Event{
				*testEvent(1, "a"),
				{Execution: runID, Seq: 2, Version: "v0.3.0"},
			},
			read:    runID,
			wantErr: errUnknownVersion,
		},
		{
			name:    "[Fail] When: the last line is cut ; Should: return errCutLine and no events",
			seed:    []sagas.Event{*testEvent(1, "a")},
			tail:    `{"Saga":"sa`,
			read:    runID,
			wantErr: errCutLine,
		},
		{
			name:    "[Fail] When: two lines have the same Seq ; Should: return errBadLine and no events",
			seed:    []sagas.Event{*testEvent(1, "a"), *testEvent(1, "b")},
			read:    runID,
			wantErr: errBadLine,
		},
		{
			name: "[Fail] When: a line belongs to another execution ; Should: return errBadLine and no events",
			seed: []sagas.Event{
				*testEvent(1, "a"),
				{Execution: "other-run", Seq: 2, Version: sagas.EventVersion},
			},
			read:    runID,
			wantErr: errBadLine,
		},
		{
			name:    "[Fail] When: a whole line is not JSON ; Should: return errBadLine and no events",
			seed:    []sagas.Event{*testEvent(1, "a")},
			tail:    "not json\n",
			read:    runID,
			wantErr: errBadLine,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			seedFile(t, dir, runID, tt.seed, tt.tail)

			events, err := newTestJournal(t, dir).Events(t.Context(), tt.read)

			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantEvents, events)
		})
	}
}

func Test_fileJournal_Append(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		event      *sagas.Event
		wantErr    error
		wantEvents int
	}{
		{
			name:       "[Success] When: the next Seq arrives after a reopen ; Should: store it after the old events",
			event:      testEvent(3, "c"),
			wantEvents: 3,
		},
		{
			name:       "[Success] When: an equal resend arrives after a reopen ; Should: store nothing",
			event:      testEvent(2, "b"),
			wantEvents: 2,
		},
		{
			name:       "[Fail] When: another event takes Seq 1 after a reopen ; Should: return ErrExecutionExists",
			event:      testEvent(1, "other"),
			wantErr:    sagas.ErrExecutionExists,
			wantEvents: 2,
		},
		{
			name:       "[Fail] When: another event takes Seq 2 after a reopen ; Should: return ErrConcurrentWriter",
			event:      testEvent(2, "other"),
			wantErr:    sagas.ErrConcurrentWriter,
			wantEvents: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			ctx := t.Context()

			first, err := openFileJournal(dir)
			require.NoError(t, err)
			require.NoError(t, first.Append(ctx, testEvent(1, "a")))
			require.NoError(t, first.Append(ctx, testEvent(2, "b")))
			require.NoError(t, first.Close())

			second := newTestJournal(t, dir)

			require.ErrorIs(t, second.Append(ctx, tt.event), tt.wantErr)

			events, err := second.Events(ctx, runID)
			require.NoError(t, err)
			assert.Len(t, events, tt.wantEvents)
		})
	}
}

func Test_journalFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		execution string
		want      string
	}{
		{
			name:      "[Success] When: the execution ID is a plain word ; Should: add the extension only",
			execution: "trip-1",
			want:      "trip-1.jsonl",
		},
		{
			name:      "[Success] When: the execution ID holds a slash ; Should: escape it",
			execution: "a/b",
			want:      "a%2Fb.jsonl",
		},
		{
			name:      "[Success] When: the execution ID climbs out of the directory ; Should: stay one file name",
			execution: "../x",
			want:      "..%2Fx.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, journalFile(tt.execution))
		})
	}
}

func Test_sameEvent(t *testing.T) {
	t.Parallel()

	zone := time.FixedZone("zone", int(time.Hour/time.Second))

	withEmpty := testEvent(1, "a")
	withEmpty.Changed, withEmpty.Steps, withEmpty.Err.Previous = []string{}, []sagas.StepZone{}, []string{}

	moved := testEvent(1, "a")
	moved.Time = moved.Time.In(zone)

	previous := testEvent(1, "a")
	previous.Err.Previous = []string{"earlier"}

	tests := []struct {
		name string
		a    *sagas.Event
		b    *sagas.Event
		want bool
	}{
		{
			name: "[Success] When: both events are equal ; Should: report equal",
			a:    testEvent(1, "a"),
			b:    testEvent(1, "a"),
			want: true,
		},
		{
			name: "[Success] When: the same instant has another zone ; Should: report equal",
			a:    testEvent(1, "a"),
			b:    moved,
			want: true,
		},
		{
			name: "[Success] When: one side has empty slices and the other nil ; Should: report equal",
			a:    testEvent(1, "a"),
			b:    withEmpty,
			want: true,
		},
		{
			name: "[Fail] When: the step differs ; Should: report different",
			a:    testEvent(1, "a"),
			b:    testEvent(1, "b"),
			want: false,
		},
		{
			name: "[Fail] When: a slice differs by content ; Should: report different",
			a:    testEvent(1, "a"),
			b:    previous,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, sameEvent(tt.a, tt.b))
		})
	}
}

func Test_openFileJournal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dir     func(t *testing.T) string
		wantErr error
	}{
		{
			name: "[Success] When: the directory exists ; Should: open the journal",
			dir:  func(t *testing.T) string { t.Helper(); return t.TempDir() },
		},
		{
			name:    "[Fail] When: the directory is missing ; Should: return fs.ErrNotExist",
			dir:     func(t *testing.T) string { t.Helper(); return filepath.Join(t.TempDir(), "missing") },
			wantErr: fs.ErrNotExist,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			j, err := openFileJournal(tt.dir(t))

			require.ErrorIs(t, err, tt.wantErr)

			if err == nil {
				require.NoError(t, j.Close())
			}
		})
	}
}

const (
	childScript = `ulimit -f "$FILEJOURNAL_LIMIT" && ` +
		`exec "$FILEJOURNAL_EXE" -test.run=Test_fileJournal_Append_failedWrite -test.v`
	childEnv     = "FILEJOURNAL_CHILD"
	limitEnv     = "FILEJOURNAL_LIMIT"
	exeEnv       = "FILEJOURNAL_EXE"
	fullEvents   = 20
	raceAttempts = 500
	cancelWait   = 50 * time.Millisecond
	dirNoRead    = 0o300
	dirReadWrite = 0o700
)

func appendUntilFull(t *testing.T) {
	t.Helper()

	ctx := t.Context()
	j := newTestJournal(t, t.TempDir())

	var failed error

	stored := 0

	for seq := 1; seq <= fullEvents && failed == nil; seq++ {
		failed = j.Append(ctx, testEvent(seq, "x"))
		if failed == nil {
			stored = seq
		}
	}

	require.Error(t, failed)

	events, err := j.Events(ctx, runID)
	require.NoError(t, err)
	assert.Len(t, events, stored)
}

func Test_fileJournal_Append_failedWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		limitBlocks int
		wantOutput  string
	}{
		{
			name:        "[Fail] When: a write passes the size limit ; Should: leave the file as it was and readable",
			limitBlocks: 1,
			wantOutput:  "--- PASS: Test_fileJournal_Append_failedWrite/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if os.Getenv(childEnv) != "" {
				appendUntilFull(t)

				return
			}

			if _, err := exec.LookPath("sh"); err != nil {
				t.Skip("the child run needs sh and ulimit")
			}

			env := append(os.Environ(),
				childEnv+"=1", exeEnv+"="+os.Args[0], limitEnv+"="+strconv.Itoa(tt.limitBlocks))
			cmd := exec.CommandContext(t.Context(), "sh", "-c", childScript)
			cmd.Env = env

			out, err := cmd.CombinedOutput()

			require.NoError(t, err, string(out))
			assert.Contains(t, string(out), tt.wantOutput)
		})
	}
}

func Test_fileJournal_Append_dirSync(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		modes    []os.FileMode
		wantErrs []error
	}{
		{
			name:     "[Success] When: the directory opens ; Should: store the first event and sync the directory",
			modes:    []os.FileMode{dirReadWrite},
			wantErrs: []error{nil},
		},
		{
			name:     "[Fail] When: the directory cannot be opened for sync ; Should: return the open error",
			modes:    []os.FileMode{dirNoRead},
			wantErrs: []error{fs.ErrPermission},
		},
		{
			name:     "[Success] When: a resend follows a failed sync and the directory opens ; Should: return nil",
			modes:    []os.FileMode{dirNoRead, dirReadWrite},
			wantErrs: []error{fs.ErrPermission, nil},
		},
		{
			name:     "[Fail] When: a resend follows a failed sync and the directory fails ; Should: return the error",
			modes:    []os.FileMode{dirNoRead, dirNoRead},
			wantErrs: []error{fs.ErrPermission, fs.ErrPermission},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if os.Geteuid() == 0 {
				t.Skip("root ignores directory permissions")
			}

			dir := t.TempDir()
			j := newTestJournal(t, dir)

			t.Cleanup(func() { assert.NoError(t, os.Chmod(dir, dirReadWrite)) })

			for i, mode := range tt.modes {
				require.NoError(t, os.Chmod(dir, mode))
				require.ErrorIs(t, j.Append(t.Context(), testEvent(1, "a")), tt.wantErrs[i])
			}
		})
	}
}

func Test_fileJournal_Append_doneWhileWaiting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wait       time.Duration
		wantErr    error
		wantStored bool
	}{
		{
			name:    "[Fail] When: the context ends while Append waits for the lock ; Should: store nothing",
			wait:    cancelWait,
			wantErr: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			j := newTestJournal(t, t.TempDir())
			ctx, cancel := context.WithCancel(t.Context())

			var wg sync.WaitGroup

			var appendErr error

			j.mu.Lock()

			wg.Go(func() { appendErr = j.Append(ctx, testEvent(1, "a")) })

			time.Sleep(tt.wait)
			cancel()
			j.mu.Unlock()
			wg.Wait()

			require.ErrorIs(t, appendErr, tt.wantErr)

			_, err := j.Events(t.Context(), runID)
			assert.Equal(t, tt.wantStored, err == nil)
		})
	}
}

func appendTwice(t *testing.T, writers [2]string) ([2]*fileJournal, bool) {
	t.Helper()

	dir := t.TempDir()

	var journals [2]*fileJournal

	for i := range journals {
		j, err := openFileJournal(dir)
		require.NoError(t, err)

		journals[i] = j
	}

	var wg sync.WaitGroup

	var errs [2]error

	start := make(chan struct{})

	for i := range journals {
		wg.Go(func() {
			e := testEvent(1, "a")
			e.Writer = writers[i]

			<-start

			errs[i] = journals[i].Append(t.Context(), e)
		})
	}

	close(start)
	wg.Wait()

	return journals, errs[0] == nil && errs[1] == nil
}

func closeAll(t *testing.T, journals [2]*fileJournal) {
	t.Helper()

	for _, j := range journals {
		require.NoError(t, j.Close())
	}
}

func Test_fileJournal_Append_twoValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		writers    [2]string
		attempts   int
		wantEvents []sagas.Event
		wantErr    error
	}{
		{
			name:     "[Fail] When: two journal values race for Seq 1 ; Should: accept both and fail on read",
			writers:  [2]string{"writer-a", "writer-b"},
			attempts: raceAttempts,
			wantErr:  errBadLine,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if runtime.GOMAXPROCS(0) < 2 {
				t.Skip("two journal values cannot race on one CPU")
			}

			for range tt.attempts {
				journals, bothAccepted := appendTwice(t, tt.writers)
				if !bothAccepted {
					closeAll(t, journals)

					continue
				}

				events, err := journals[0].Events(t.Context(), runID)

				closeAll(t, journals)
				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, tt.wantEvents, events)

				return
			}

			t.Fatal("no attempt let both journal values accept Seq 1")
		})
	}
}
