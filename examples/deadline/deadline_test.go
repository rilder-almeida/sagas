package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

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
			name:      "[Success] When: the writer accepts all writes ; Should: print the five runs",
			newWriter: bufferWriter,
			wantOutput: "grace-long: compensated, pending: [], undone: [invoice pack reserve tag]\n" +
				"grace-short: compensation_failed, pending: [reserve pack tag], undone: [invoice], grace ended\n" +
				"block: compensation_failed, pending: [reserve invoice], undone: [pack tag]\n" +
				"continue: compensation_failed, pending: [invoice], undone: [pack reserve tag]\n" +
				"halt: compensation_failed, pending: [reserve invoice pack], undone: [tag]\n",
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

const (
	invoiceStep = "invoice"
	packStep    = "pack"
	reserveStep = "reserve"
	tagStep     = "tag"
)

func Test_newSaga(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		grace        time.Duration
		policy       sagas.FailurePolicy
		fault        string
		wantReport   string
		wantHeld     []string
		wantGraceEnd bool
	}{
		{
			name:       "[Success] When: the grace period is long enough ; Should: undo every step after the deadline",
			grace:      longGrace,
			wantReport: "t-1: compensated, pending: [], undone: [invoice pack reserve tag]\n",
			wantHeld:   []string{},
		},
		{
			name:         "[Fail] When: a compensation outlasts the grace ; Should: end with ErrGraceEnded",
			grace:        shortGrace,
			fault:        faultSlowUndo,
			wantReport:   "t-1: compensation_failed, pending: [reserve pack tag], undone: [invoice], grace ended\n",
			wantHeld:     []string{packStep, reserveStep, tagStep},
			wantGraceEnd: true,
		},
		{
			name:       "[Fail] When: a compensation fails under block ; Should: block the steps that wait on it",
			grace:      longGrace,
			policy:     sagas.Block,
			fault:      faultInvoiceUndo,
			wantReport: "t-1: compensation_failed, pending: [reserve invoice], undone: [pack tag]\n",
			wantHeld:   []string{invoiceStep, reserveStep},
		},
		{
			name:       "[Fail] When: a compensation fails under continue ; Should: undo every other step",
			grace:      longGrace,
			policy:     sagas.Continue,
			fault:      faultInvoiceUndo,
			wantReport: "t-1: compensation_failed, pending: [invoice], undone: [pack reserve tag]\n",
			wantHeld:   []string{invoiceStep},
		},
		{
			name:       "[Fail] When: a compensation fails under halt ; Should: halt every compensation not started",
			grace:      longGrace,
			policy:     sagas.BlockAll,
			fault:      faultInvoiceUndo,
			wantReport: "t-1: compensation_failed, pending: [reserve invoice pack], undone: [tag]\n",
			wantHeld:   []string{invoiceStep, packStep, reserveStep},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newServices()

			saga, err := svc.build(tt.grace, tt.policy)
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(t.Context(), callerTimeout)
			defer cancel()

			o := order{ID: "t-1", Fault: tt.fault}
			runErr := saga.Run(ctx, &o)

			require.Error(t, ctx.Err())

			var out bytes.Buffer

			require.NoError(t, report(&out, &o, svc.undone.take(), runErr))
			assert.Equal(t, tt.wantReport, out.String())
			assert.Equal(t, tt.wantGraceEnd, errors.Is(runErr, sagas.ErrGraceEnded))

			held := []string{}

			for _, probe := range []struct {
				step  string
				store *store
				first string
			}{
				{invoiceStep, svc.ledger, "INV-1"},
				{packStep, svc.dock, "PCK-1"},
				{reserveStep, svc.stock, "STK-1"},
				{tagStep, svc.labels, "LBL-1"},
			} {
				key := sagas.CallInfo{Saga: "deadline", Execution: o.ID, Step: probe.step}.IdempotencyKey()
				id, putErr := probe.store.put(t.Context(), key)

				require.NoError(t, putErr)

				if id == probe.first {
					held = append(held, probe.step)
				}
			}

			assert.Equal(t, tt.wantHeld, held)
		})
	}
}

func Test_newSaga_blockAllRepeat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scenario string
		runs     int
		want     []string
	}{
		{
			name:     "[Success] When: the halt scenario runs with no gate many times ; Should: print one report line",
			scenario: "halt",
			runs:     100,
			want:     []string{"halt: compensation_failed, pending: [reserve invoice pack], undone: [tag]\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runs := policyRuns()
			sc := runs[slices.IndexFunc(runs, func(s scenario) bool { return s.name == tt.scenario })]
			lines := make([]string, tt.runs)
			errs := make([]error, tt.runs)

			var wg sync.WaitGroup

			for i := range lines {
				wg.Go(func() {
					svc := newServices()

					saga, err := newSaga(svc, longGrace, sagas.BlockAll)
					if err != nil {
						errs[i] = err

						return
					}

					ctx, cancel := context.WithTimeout(t.Context(), callerTimeout)
					defer cancel()

					o := order{ID: sc.name, Fault: sc.fault}
					runErr := saga.Run(ctx, &o)

					var out bytes.Buffer

					errs[i] = report(&out, &o, svc.undone.take(), runErr)
					lines[i] = out.String()
				})
			}

			wg.Wait()

			require.NoError(t, errors.Join(errs...))

			slices.Sort(lines)
			assert.Equal(t, tt.want, slices.Compact(lines))
		})
	}
}
