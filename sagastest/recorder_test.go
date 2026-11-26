package sagastest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type groupBuilder func(items ...sagas.Flow) *sagas.Group

func runRecorded(t *testing.T, group groupBuilder, inner []sagas.Middleware, steps ...sagas.Flow) ([]Attempt, error) {
	t.Helper()

	rec := NewRecorder()
	mw := append([]sagas.Middleware{rec}, inner...)

	saga, err := sagas.New[probeState]("recorded", group(steps...), sagas.WithMiddleware(mw...))
	require.NoError(t, err)

	runErr := saga.Run(context.Background(), &probeState{ID: runID})

	return rec.Attempts(), runErr
}

func withoutErr(attempts []Attempt) []Attempt {
	out := slices.Clone(attempts)
	for i := range out {
		out[i].Err = nil
	}

	return out
}

func idempotencyKeys(attempts []Attempt) []string {
	keys := make([]string, len(attempts))
	for i, c := range attempts {
		keys[i] = c.IdempotencyKey()
	}

	return keys
}

func errMessages(attempts []Attempt) []string {
	msgs := make([]string, len(attempts))
	for i, c := range attempts {
		if c.Err != nil {
			msgs[i] = c.Err.Error()
		}
	}

	return msgs
}

func assertSameErr(t *testing.T, want, got error) {
	t.Helper()

	if want == nil {
		assert.NoError(t, got)

		return
	}

	assert.ErrorIs(t, got, want)
}

type panicProbe struct {
	sagas.Step[probeState, sagas.None]
}

func newPanicProbe(name string) *panicProbe {
	p := &panicProbe{}
	p.Name, p.RetryPolicy = name, sagas.NoRetry()

	return p
}

func (*panicProbe) Execute(context.Context, probeState) error { panic(errBoom) }

const (
	keyA = "recorded/run-1/a"
	keyB = "recorded/run-1/b"
	keyC = "recorded/run-1/c"
)

func Test_NewRecorder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		names    []string
		wantSeen int
	}{
		{
			name:     "[Success] When: nothing ran ; Should: return no calls",
			names:    nil,
			wantSeen: 0,
		},
		{
			name:     "[Success] When: another recorder saw three steps ; Should: return no calls",
			names:    []string{"a", "b", "c"},
			wantSeen: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fresh := NewRecorder()

			if len(tt.names) > 0 {
				steps := make([]sagas.Flow, len(tt.names))
				for i, name := range tt.names {
					steps[i] = plain(name)
				}

				attempts, err := runRecorded(t, sagas.Sequence, nil, steps...)
				require.NoError(t, err)
				assert.Len(t, attempts, tt.wantSeen)
			}

			assert.Empty(t, fresh.Attempts())
		})
	}
}

func recorded(step string, phase sagas.Phase, attempt int, err error) Attempt {
	return Attempt{Saga: "recorded", Execution: runID, Step: step, Phase: phase, Attempt: attempt, Err: err}
}

func Test_Recorder_Wrap(t *testing.T) {
	t.Parallel()

	exec := func(step string, attempt int, err error) Attempt {
		return recorded(step, sagas.PhaseExecute, attempt, err)
	}

	tests := []struct {
		name         string
		flow         groupBuilder
		build        func() []sagas.Flow
		inner        []sagas.Middleware
		wantAttempts []Attempt
		wantKeys     []string
		wantMsgs     []string
		wantErr      bool
		wantPanic    bool
		wantCause    bool
		unordered    bool
	}{
		{
			name: "[Success] When: three steps run in sequence ; " +
				"Should: record them in order, each with the key of its step",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{
					plain("a"), plain("b"), plain("c"),
				}
			},
			wantAttempts: []Attempt{exec("a", 1, nil), exec("b", 1, nil), exec("c", 1, nil)},
			wantKeys:     []string{keyA, keyB, keyC},
			wantMsgs:     []string{"", "", ""},
		},
		{
			name: "[Fail] When: the last step fails ; Should: record the error it returned",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{
					plain("a"), plain("b"), newProbe("c", sagas.NoRetry(), errBoom),
				}
			},
			wantAttempts: []Attempt{exec("a", 1, nil), exec("b", 1, nil), exec("c", 1, errBoom)},
			wantKeys:     []string{keyA, keyB, keyC},
			wantMsgs:     []string{"", "", errBoom.Error()},
			wantErr:      true,
		},
		{
			name: "[Success] When: two steps run in parallel ; Should: record both without a data race",
			flow: sagas.Parallel,
			build: func() []sagas.Flow {
				return []sagas.Flow{plain("a"), plain("b")}
			},
			wantAttempts: []Attempt{exec("a", 1, nil), exec("b", 1, nil)},
			wantKeys:     []string{keyA, keyB},
			wantMsgs:     []string{"", ""},
			unordered:    true,
		},
		{
			name: "[Fail] When: a step retries ; Should: record each attempt number with its error",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{newProbe("a", fastRetry(), errBoom)}
			},
			wantAttempts: []Attempt{exec("a", 1, errBoom), exec("a", 2, errBoom)},
			wantKeys:     []string{keyA, keyA},
			wantMsgs:     []string{errBoom.Error(), errBoom.Error()},
			wantErr:      true,
		},
		{
			name: "[Fail] When: a later step fails ; Should: record the compensation of the earlier step",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{newUndo("a", nil), newProbe("b", sagas.NoRetry(), errBoom)}
			},
			wantAttempts: []Attempt{
				exec("a", 1, nil),
				exec("b", 1, errBoom),
				recorded("a", sagas.PhaseCompensate, 1, nil),
			},
			wantKeys: []string{keyA, keyB, keyA},
			wantMsgs: []string{"", errBoom.Error(), ""},
			wantErr:  true,
		},
		{
			name: "[Success] When: a step has a pivot ; Should: record the pivot phase",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{newPivot("a")}
			},
			wantAttempts: []Attempt{recorded("a", sagas.PhasePivot, 1, nil)},
			wantKeys:     []string{keyA},
			wantMsgs:     []string{""},
		},
		{
			name: "[Fail] When: the call panics below the recorder ; " +
				"Should: record it with a panic error and let the run see the panic",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{plain("a")}
			},
			inner:        []sagas.Middleware{Panic("a")},
			wantAttempts: []Attempt{exec("a", 1, sagas.ErrPanic)},
			wantKeys:     []string{keyA},
			wantMsgs:     []string{`sagas: panic: sagastest: injected panic in step "a"`},
			wantErr:      true,
			wantPanic:    true,
			wantCause:    true,
		},
		{
			name: "[Fail] When: the step method panics ; " +
				"Should: record it with an error that wraps sagas.ErrPanic and keeps the panic error",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{newPanicProbe("a")}
			},
			wantAttempts: []Attempt{exec("a", 1, sagas.ErrPanic)},
			wantKeys:     []string{keyA},
			wantMsgs:     []string{"sagas: panic: " + errBoom.Error()},
			wantErr:      true,
			wantPanic:    true,
			wantCause:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			attempts, err := runRecorded(t, tt.flow, tt.inner, tt.build()...)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			var panicked *sagas.PanicError
			assert.Equal(t, tt.wantPanic, errors.As(err, &panicked))

			if tt.unordered {
				assert.ElementsMatch(t, withoutErr(tt.wantAttempts), withoutErr(attempts))
				assert.ElementsMatch(t, tt.wantKeys, idempotencyKeys(attempts))
			} else {
				assert.Equal(t, withoutErr(tt.wantAttempts), withoutErr(attempts))
				assert.Equal(t, tt.wantKeys, idempotencyKeys(attempts))
			}

			assert.Equal(t, tt.wantMsgs, errMessages(attempts))

			for _, want := range tt.wantAttempts {
				i := slices.IndexFunc(attempts, func(c Attempt) bool {
					return c.Step == want.Step && c.Phase == want.Phase && c.Attempt == want.Attempt
				})
				require.GreaterOrEqual(t, i, 0)
				assertSameErr(t, want.Err, attempts[i].Err)

				var cause *sagas.PanicError
				assert.Equal(t, tt.wantCause, errors.As(attempts[i].Err, &cause))
			}
		})
	}
}

func Test_Recorder_Attempts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		corrupt func([]Attempt)
	}{
		{
			name:    "[Success] When: the caller rewrites an element ; Should: leave a second Attempts unchanged",
			corrupt: func(c []Attempt) { c[0].Step = "changed" },
		},
		{
			name: "[Success] When: the caller truncates and appends ; Should: leave a second Attempts unchanged",
			corrupt: func(c []Attempt) {
				_ = append(c[:0], recorded("x", sagas.PhaseExecute, 1, nil))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := NewRecorder()
			probe := plain("a")
			saga, err := sagas.New[probeState]("recorded", probe, sagas.WithMiddleware(rec))
			require.NoError(t, err)
			require.NoError(t, saga.Run(context.Background(), &probeState{ID: runID}))

			first := rec.Attempts()
			want := []Attempt{recorded("a", sagas.PhaseExecute, 1, nil)}
			require.Equal(t, want, first)
			require.Equal(t, keyA, first[0].IdempotencyKey())

			tt.corrupt(first)

			assert.Equal(t, want, rec.Attempts())
		})
	}
}

func Test_recordedPanicError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "[Success] When: the panic value is an error ; Should: print the message of the panic error",
			value: errBoom,
			want:  "sagas: panic: boom",
		},
		{
			name:  "[Success] When: the panic value is a string ; Should: print the message of the panic error",
			value: "stop",
			want:  "sagas: panic: stop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := &recordedPanicError{err: &sagas.PanicError{Value: tt.value}}

			assert.Equal(t, tt.want, err.Error())
		})
	}
}

func Test_recordedPanicError_Unwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		inner error
	}{
		{
			name:  "[Success] When: it holds a panic error ; Should: return sagas.ErrPanic and the panic error",
			inner: &sagas.PanicError{Value: errBoom},
		},
		{
			name:  "[Success] When: it holds a wrapped panic error ; Should: return sagas.ErrPanic and the wrapper",
			inner: fmt.Errorf("outer: %w", &sagas.PanicError{Value: "stop"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := &recordedPanicError{err: tt.inner}

			assert.Equal(t, []error{sagas.ErrPanic, tt.inner}, err.Unwrap())
		})
	}
}
