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
			name:      "[Success] When: the writer accepts all writes ; Should: print the four runs",
			newWriter: bufferWriter,
			wantOutput: "pay-1: completed, provider calls: 3, refunds: 0\n" +
				"pay-2: compensated, pending: [], outcome: failed, provider calls: 1, refunds: 1\n" +
				"pay-3: compensated, pending: [], outcome: unknown, provider calls: 3, refunds: 1\n" +
				"pay-4: compensated, pending: [], outcome: failed, provider calls: 0, refunds: 1\n",
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
		name        string
		cards       []string
		wantReports string
		wantOpen    bool
	}{
		{
			name:        "[Success] When: the provider fails twice then answers ; Should: complete on the third call",
			cards:       []string{cardFlaky},
			wantReports: "pay-1: completed, provider calls: 3, refunds: 0\n",
		},
		{
			name:        "[Fail] When: the card is refused ; Should: stop after one call and refund once",
			cards:       []string{cardRefused},
			wantReports: "pay-1: compensated, pending: [], outcome: failed, provider calls: 1, refunds: 1\n",
		},
		{
			name:        "[Fail] When: the provider never answers ; Should: end unknown and refund the charge",
			cards:       []string{cardSilent},
			wantReports: "pay-1: compensated, pending: [], outcome: unknown, provider calls: 3, refunds: 1\n",
		},
		{
			name:  "[Fail] When: the breaker opened on earlier runs ; Should: reject without a call and still refund",
			cards: []string{cardRefused, cardSilent, cardFlaky},
			wantReports: "pay-1: compensated, pending: [], outcome: failed, provider calls: 1, refunds: 1\n" +
				"pay-2: compensated, pending: [], outcome: unknown, provider calls: 3, refunds: 1\n" +
				"pay-3: compensated, pending: [], outcome: failed, provider calls: 0, refunds: 1\n",
			wantOpen: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newServices()

			saga, err := newSaga(svc)
			require.NoError(t, err)

			var (
				out    bytes.Buffer
				runErr error
			)

			for i, card := range tt.cards {
				o := order{ID: fmt.Sprintf("pay-%d", i+1), Card: card}
				runErr = saga.Run(t.Context(), &o)

				require.NoError(t, report(&out, &o, svc.provider.take(), runErr))
			}

			assert.Equal(t, tt.wantReports, out.String())
			assert.Equal(t, tt.wantOpen, errors.Is(runErr, errOpen))
		})
	}
}

type probe struct {
	phase sagas.Phase
	err   error
	wait  time.Duration
}

var errDown = errors.New("service down")

func Test_breaker_Wrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		limit    int
		cooldown time.Duration
		probes   []probe
		wantRan  []bool
		wantErrs []error
	}{
		{
			name:     "[Fail] When: failures reach the limit ; Should: reject the next call without running it",
			limit:    2,
			cooldown: time.Minute,
			probes: []probe{
				{phase: sagas.PhaseExecute, err: errDown},
				{phase: sagas.PhaseExecute, err: errDown},
				{phase: sagas.PhaseExecute},
			},
			wantRan:  []bool{true, true, false},
			wantErrs: []error{errDown, errDown, errOpen},
		},
		{
			name:     "[Success] When: the breaker is open ; Should: let a compensation through",
			limit:    1,
			cooldown: time.Minute,
			probes: []probe{
				{phase: sagas.PhaseExecute, err: errDown},
				{phase: sagas.PhaseCompensate},
			},
			wantRan:  []bool{true, true},
			wantErrs: []error{errDown, nil},
		},
		{
			name:     "[Success] When: a success separates failures ; Should: count again from zero and stay closed",
			limit:    2,
			cooldown: time.Minute,
			probes: []probe{
				{phase: sagas.PhaseExecute, err: errDown},
				{phase: sagas.PhaseExecute},
				{phase: sagas.PhaseExecute, err: errDown},
				{phase: sagas.PhaseExecute},
			},
			wantRan:  []bool{true, true, true, true},
			wantErrs: []error{errDown, nil, errDown, nil},
		},
		{
			name:     "[Success] When: the cooldown has passed ; Should: run calls again",
			limit:    1,
			cooldown: 10 * time.Millisecond,
			probes: []probe{
				{phase: sagas.PhaseExecute, err: errDown},
				{phase: sagas.PhaseExecute, wait: 30 * time.Millisecond},
			},
			wantRan:  []bool{true, true},
			wantErrs: []error{errDown, nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := &breaker{limit: tt.limit, cooldown: tt.cooldown}
			ran := make([]bool, 0, len(tt.probes))
			errs := make([]error, 0, len(tt.probes))

			for _, p := range tt.probes {
				time.Sleep(p.wait)

				didRun := false
				err := b.Wrap(t.Context(), sagas.CallInfo{Phase: p.phase}, func(context.Context) error {
					didRun = true

					return p.err
				})

				ran = append(ran, didRun)
				errs = append(errs, err)
			}

			assert.Equal(t, tt.wantRan, ran)
			assert.Equal(t, tt.wantErrs, errs)
		})
	}
}

func Test_rateLimit_Wrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		every          time.Duration
		calls          int
		timeout        time.Duration
		wantRan        int
		wantMinElapsed time.Duration
		wantErr        error
	}{
		{
			name:           "[Success] When: calls arrive back to back ; Should: space their starts by the interval",
			every:          10 * time.Millisecond,
			calls:          3,
			timeout:        time.Minute,
			wantRan:        3,
			wantMinElapsed: 20 * time.Millisecond,
		},
		{
			name:           "[Fail] When: the context ends during the wait ; Should: return its error without running",
			every:          time.Hour,
			calls:          2,
			timeout:        20 * time.Millisecond,
			wantRan:        1,
			wantMinElapsed: 20 * time.Millisecond,
			wantErr:        context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			started := time.Now()

			ctx, cancel := context.WithTimeout(t.Context(), tt.timeout)
			defer cancel()

			limit := &rateLimit{every: tt.every}
			ran := 0

			var err error

			for range tt.calls {
				err = limit.Wrap(ctx, sagas.CallInfo{}, func(context.Context) error {
					ran++

					return nil
				})
			}

			assert.Equal(t, tt.wantRan, ran)
			assert.GreaterOrEqual(t, time.Since(started), tt.wantMinElapsed)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func Test_outcomeOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		card        string
		cancelled   bool
		wantOutcome string
	}{
		{
			name:        "[Success] When: a step fails ; Should: name the kind of its error",
			card:        cardRefused,
			wantOutcome: "failed",
		},
		{
			name:        "[Fail] When: the caller context ended before the step started ; Should: name no outcome",
			card:        cardFlaky,
			cancelled:   true,
			wantOutcome: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			saga, err := newSaga(newServices())
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			if tt.cancelled {
				cancel()
			}

			runErr := saga.Run(ctx, &order{ID: "pay-1", Card: tt.card})

			var failed *sagas.RunError

			require.ErrorAs(t, runErr, &failed)
			assert.Equal(t, tt.wantOutcome, outcomeOf(failed))
		})
	}
}
