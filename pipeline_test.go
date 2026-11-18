package sagas

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_PanicError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "[Success] When: the value is a string ; Should: print the string",
			value: "bad",
			want:  "sagas: panic: bad",
		},
		{
			name:  "[Success] When: the value is an error ; Should: print the error message",
			value: errBoom,
			want:  "sagas: panic: boom",
		},
		{
			name:  "[Success] When: the value is nil ; Should: print nil",
			value: nil,
			want:  "sagas: panic: <nil>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, (&PanicError{Value: tt.value, Stack: []byte("stack")}).Error())
		})
	}
}

func panicWith(v any) { panic(v) }

func Test_recoverer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		next      Handler
		wantErr   error
		wantPanic bool
		wantText  string
	}{
		{
			name:    "[Success] When: the handler returns nil ; Should: return nil",
			next:    func(context.Context) error { return nil },
			wantErr: nil,
		},
		{
			name:    "[Fail] When: the handler returns an error ; Should: pass the same error through",
			next:    func(context.Context) error { return errBoom },
			wantErr: errBoom,
		},
		{
			name:      "[Fail] When: the handler panics with a string ; Should: return a PanicError with the value",
			next:      func(context.Context) error { panic("bad") },
			wantPanic: true,
			wantText:  "sagas: panic: bad",
		},
		{
			name:      "[Fail] When: the handler panics with an error ; Should: return a PanicError with the value",
			next:      func(context.Context) error { panic(errBoom) },
			wantPanic: true,
			wantText:  "sagas: panic: boom",
		},
		{
			name: "[Fail] When: the handler panics with nil ; Should: still return a PanicError",
			next: func(context.Context) error {
				panicWith(nil)

				return nil
			},
			wantPanic: true,
			wantText:  "sagas: panic: runtime error: panic called with nil argument",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := recoverer(tt.next)(t.Context())

			if !tt.wantPanic {
				require.Equal(t, tt.wantErr, err)

				return
			}

			var panicked *PanicError

			require.ErrorAs(t, err, &panicked)
			assert.Equal(t, tt.wantText, err.Error())
			assert.NotEmpty(t, panicked.Stack)
			assert.NotNil(t, panicked.Value)
		})
	}
}

func Test_classify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want outcomeKind
	}{
		{
			name: "[Success] When: the error is nil ; Should: be a success",
			err:  nil,
			want: outcomeSuccess,
		},
		{
			name: "[Fail] When: the error is plain ; Should: be a failure",
			err:  errBoom,
			want: outcomeFailure,
		},
		{
			name: "[Fail] When: the error is permanent ; Should: be a failure",
			err:  Permanent(errBoom),
			want: outcomeFailure,
		},
		{
			name: "[Fail] When: the error is context.Canceled ; Should: be unknown",
			err:  context.Canceled,
			want: outcomeUnknown,
		},
		{
			name: "[Fail] When: a wrapped DeadlineExceeded ; Should: be unknown",
			err:  fmt.Errorf("x: %w", context.DeadlineExceeded),
			want: outcomeUnknown,
		},
		{
			name: "[Fail] When: the error is a PanicError ; Should: be unknown",
			err:  &PanicError{Value: "bad"},
			want: outcomeUnknown,
		},
		{
			name: "[Fail] When: a permanent error wraps a PanicError ; Should: be unknown",
			err:  Permanent(&PanicError{Value: "bad"}),
			want: outcomeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, classify(tt.err))
		})
	}
}

type pipeState struct {
	A int
	B string
	C bool
}

type recorder struct {
	mu    sync.Mutex
	trace []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.trace = append(r.trace, s)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.trace)
}

func baseOf(tb testing.TB, s pipeState) snapshot[pipeState] {
	tb.Helper()

	st, err := newStore(s)
	require.NoError(tb, err)

	return st.snapshot()
}

func tagged(r *recorder, name string) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			r.add(name + ">")

			err := next(ctx)

			r.add("<" + name)

			return err
		}
	}
}

func probe(r *recorder) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			if _, ok := Info(ctx); ok {
				r.add("info")
			}

			if _, ok := ctx.Deadline(); ok {
				r.add("deadline")
			}

			return next(ctx)
		}
	}
}

func recordCall(r *recorder) func(context.Context, *pipeState) error {
	return func(context.Context, *pipeState) error {
		r.add("call")

		return nil
	}
}

func failWith(err error) func(*recorder) func(context.Context, *pipeState) error {
	return func(*recorder) func(context.Context, *pipeState) error {
		return func(context.Context, *pipeState) error { return err }
	}
}

func attemptOf(ctx context.Context) int {
	si, _ := Info(ctx)

	return si.Attempt
}

func pipelineOrderRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: three user middlewares wrap the call ; Should: run the declared order inside " +
			"Info and the deadline",
		spec: actionSpec(),
		middleware: func(r *recorder) []Middleware {
			return []Middleware{tagged(r, "A"), probe(r), tagged(r, "B")}
		},
		call:         recordCall,
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{},
		wantTrace:    []string{"A>", "info", "deadline", "B>", "call", "<B", "<A"},
	}
}

func pipelineUserPanicRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: a user middleware panics ; Should: return a PanicError as unknown without a retry",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(Handler) Handler {
				return func(context.Context) error { panic("bad middleware") }
			}}
		},
		call:         recordCall,
		wantKind:     outcomeUnknown,
		wantAttempts: 1,
		wantPanic:    true,
	}
}

func pipelineFreshCopyRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: attempt 1 changes a field and fails ; Should: give attempt 2 the base and " +
			"return only attempt 2's change",
		spec: actionSpec(),
		base: pipeState{A: 1},
		call: func(*recorder) func(context.Context, *pipeState) error {
			return func(ctx context.Context, s *pipeState) error {
				if attemptOf(ctx) == 1 {
					s.A = 7

					return errBoom
				}

				s.B = fmt.Sprintf("a=%d", s.A)

				return nil
			}
		},
		wantKind:     outcomeSuccess,
		wantAttempts: 2,
		wantState:    &pipeState{A: 1, B: "a=1"},
	}
}

func pipelineTimeoutRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: every action attempt runs to its deadline ; Should: end unknown after 3 attempts",
		spec: actionSpec(),
		call: func(*recorder) func(context.Context, *pipeState) error {
			return func(ctx context.Context, _ *pipeState) error {
				<-ctx.Done()

				return ctx.Err()
			}
		},
		wantKind:     outcomeUnknown,
		wantAttempts: 3,
		wantErr:      context.DeadlineExceeded,
	}
}

func pipelineCompensationRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: a compensation has a timeout set ; Should: run without a deadline",
		spec: compensationSpec(),
		call: func(r *recorder) func(context.Context, *pipeState) error {
			return func(ctx context.Context, _ *pipeState) error {
				if _, ok := ctx.Deadline(); !ok {
					r.add("no-deadline")
				}

				return nil
			}
		},
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{},
		wantTrace:    []string{"no-deadline"},
	}
}

func pipelineSuccessRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: the call changes the state and returns nil ; Should: return success and the copy",
		spec: actionSpec(),
		base: pipeState{A: 1},
		call: func(*recorder) func(context.Context, *pipeState) error {
			return func(_ context.Context, s *pipeState) error {
				s.C = true

				return nil
			}
		},
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{A: 1, C: true},
	}
}

func pipelineFailureRow() pipelineRow {
	return pipelineRow{
		name:         "[Fail] When: every attempt returns a plain error ; Should: fail after 3 attempts",
		spec:         actionSpec(),
		call:         failWith(errBoom),
		wantKind:     outcomeFailure,
		wantAttempts: 3,
		wantErr:      errBoom,
	}
}

func pipelinePermanentRow() pipelineRow {
	return pipelineRow{
		name:         "[Fail] When: the call returns a permanent error ; Should: stop after one attempt as failure",
		spec:         actionSpec(),
		call:         failWith(Permanent(errBoom)),
		wantKind:     outcomeFailure,
		wantAttempts: 1,
		wantErr:      errBoom,
	}
}

func pipelineCallPanicRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: the call panics ; Should: return unknown with a PanicError after one attempt",
		spec: actionSpec(),
		call: func(*recorder) func(context.Context, *pipeState) error {
			return func(context.Context, *pipeState) error { panic("bad call") }
		},
		wantKind:     outcomeUnknown,
		wantAttempts: 1,
		wantPanic:    true,
	}
}

func pipelineNotRunRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: a middleware returns nil without calling next ; Should: fail with ErrCallNotRun",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(Handler) Handler {
				return func(context.Context) error { return nil }
			}}
		},
		call:         recordCall,
		wantKind:     outcomeFailure,
		wantAttempts: 1,
		wantErr:      ErrCallNotRun,
	}
}

func pipelineTwiceRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: a middleware calls next twice ; Should: run the call twice in one attempt",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(next Handler) Handler {
				return func(ctx context.Context) error {
					return errors.Join(next(ctx), next(ctx))
				}
			}}
		},
		call:         recordCall,
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{},
		wantTrace:    []string{"call", "call"},
	}
}

type pipelineRow struct {
	name         string
	spec         callSpec
	base         pipeState
	corruptBase  bool
	middleware   func(r *recorder) []Middleware
	call         func(r *recorder) func(context.Context, *pipeState) error
	wantKind     outcomeKind
	wantAttempts int
	wantErr      error
	wantPanic    bool
	wantState    *pipeState
	wantTrace    []string
}

func actionSpec() callSpec {
	return callSpec{
		saga: "s", step: "pay", phase: PhaseAction, timeout: time.Second,
		retry: RetryPolicy{Attempts: 3, Initial: time.Millisecond, NoJitter: true},
	}
}

func compensationSpec() callSpec {
	spec := actionSpec()
	spec.phase = PhaseCompensation

	return spec
}

func baseFor(t *testing.T, row *pipelineRow) snapshot[pipeState] {
	t.Helper()

	base := baseOf(t, row.base)
	if row.corruptBase {
		base.fields[0] = []byte("not json")
	}

	return base
}

func assertCallErr(t *testing.T, got, want error) {
	t.Helper()

	if want == nil {
		require.NoError(t, got)

		return
	}

	require.ErrorIs(t, got, want)
}

func Test_pipeline(t *testing.T) {
	t.Parallel()

	tests := []pipelineRow{
		pipelineOrderRow(),
		pipelineUserPanicRow(),
		pipelineFreshCopyRow(),
		pipelineTimeoutRow(),
		pipelineCompensationRow(),
		pipelineSuccessRow(),
		pipelineFailureRow(),
		pipelinePermanentRow(),
		pipelineCallPanicRow(),
		pipelineNotRunRow(),
		pipelineTwiceRow(),
		pipelineCorruptBaseRow(),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				rec := &recorder{}
				spec := tt.spec

				if tt.middleware != nil {
					spec.middleware = tt.middleware(rec)
				}

				got := pipeline(&spec, tt.call(rec))(t.Context(), "exec-1", baseFor(t, &tt))

				assert.Equal(t, tt.wantKind, got.kind)
				assert.Equal(t, tt.wantAttempts, got.attempts)
				assert.Equal(t, tt.wantState, got.state)
				assert.Equal(t, tt.wantTrace, rec.snapshot())

				if tt.wantPanic {
					require.ErrorAs(t, got.err, new(*PanicError))

					return
				}

				assertCallErr(t, got.err, tt.wantErr)
			})
		})
	}
}

func pipelineCorruptBaseRow() pipelineRow {
	return pipelineRow{
		name:         "[Fail] When: the base cannot be decoded ; Should: fail permanently without running the call",
		spec:         actionSpec(),
		call:         recordCall,
		corruptBase:  true,
		wantKind:     outcomeFailure,
		wantAttempts: 1,
		wantErr:      ErrInvalidState,
	}
}

func Benchmark_pipeline(b *testing.B) {
	spec := callSpec{saga: "s", step: "pay", phase: PhaseAction, retry: RetryPolicy{Attempts: 1}}
	call := pipeline(&spec, func(_ context.Context, s *pipeState) error {
		s.A++

		return nil
	})
	base := baseOf(b, pipeState{A: 1, B: "x"})
	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		if got := call(ctx, "exec-1", base); got.kind != outcomeSuccess {
			b.Fatal(got.err)
		}
	}
}
