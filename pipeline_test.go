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
			name: "[Fail] When: a permanent error wraps ErrCallUnfinished ; Should: be unknown",
			err:  Permanent(ErrCallUnfinished),
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

var errCall = errors.New("call failed")

type pipeState struct {
	A int
	B string
	C bool
}

type badState struct {
	A string
	B string
	C string
}

func swallow(error) error { return nil }

func discard(error) {}

func lastErr(_, last error) error { return last }

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
		wantErrs:     []error{context.DeadlineExceeded},
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
		wantErrs:     []error{errBoom},
	}
}

func pipelinePermanentRow() pipelineRow {
	return pipelineRow{
		name:         "[Fail] When: the call returns a permanent error ; Should: stop after one attempt as failure",
		spec:         actionSpec(),
		call:         failWith(Permanent(errBoom)),
		wantKind:     outcomeFailure,
		wantAttempts: 1,
		wantErrs:     []error{errBoom},
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

func observePanic(r *recorder) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			err := next(ctx)

			if panicked, ok := errors.AsType[*PanicError](err); ok {
				r.add(fmt.Sprint("panic:", panicked.Value))
			}

			return err
		}
	}
}

func pipelineMiddlewareSeesPanicRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: the call panics ; Should: give middleware a PanicError and return unknown after 1 attempt",
		spec: actionSpec(),
		middleware: func(r *recorder) []Middleware {
			return []Middleware{observePanic(r)}
		},
		call: func(*recorder) func(context.Context, *pipeState) error {
			return func(context.Context, *pipeState) error { panic("bad call") }
		},
		wantKind:     outcomeUnknown,
		wantAttempts: 1,
		wantPanic:    true,
		wantTrace:    []string{"panic:bad call"},
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
		wantErrs:     []error{ErrCallNotRun},
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

func abandoning(start, limit time.Duration) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			errc := make(chan error, 1)

			go func() {
				time.Sleep(start)

				errc <- next(ctx)
			}()

			timer := time.NewTimer(limit)
			defer timer.Stop()

			select {
			case err := <-errc:
				return err
			case <-timer.C:
				return context.DeadlineExceeded
			}
		}
	}
}

const slowMark = "slow call"

func slowCall(r *recorder) func(context.Context, *pipeState) error {
	return func(ctx context.Context, s *pipeState) error {
		time.Sleep(20 * time.Millisecond)

		s.A = attemptOf(ctx)

		r.add(slowMark)

		return nil
	}
}

func lateStart(ctx context.Context, next Handler) {
	go func() {
		time.Sleep(time.Millisecond)

		discard(next(ctx))
	}()
}

func pipelineAbandonedRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: middleware returns DeadlineExceeded while attempt 1's call runs ; " +
			"Should: end unknown after 1 attempt with both errors and one action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{abandoning(0, 10*time.Millisecond)}
		},
		call:         slowCall,
		wantKind:     outcomeUnknown,
		wantAttempts: 1,
		wantErrs:     []error{context.DeadlineExceeded, ErrCallUnfinished},
		wantTrace:    []string{slowMark},
	}
}

func pipelineAbandonedThenSkipRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: attempt 1's goroutine reaches next after a timeout and attempt 2 returns nil " +
			"without next ; Should: fail with ErrCallNotRun and run no action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			first := abandoning(15*time.Millisecond, 10*time.Millisecond)

			return []Middleware{func(next Handler) Handler {
				return func(ctx context.Context) error {
					if attemptOf(ctx) == 1 {
						return first(next)(ctx)
					}

					time.Sleep(30 * time.Millisecond)

					return nil
				}
			}}
		},
		call:         slowCall,
		wantKind:     outcomeFailure,
		wantAttempts: 2,
		wantErrs:     []error{ErrCallNotRun},
	}
}

func pipelineUnfinishedRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: middleware starts next in a goroutine and returns nil while the call runs ; " +
			"Should: end unknown with ErrCallUnfinished after 1 attempt",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(next Handler) Handler {
				return func(ctx context.Context) error {
					go func() { discard(next(ctx)) }()

					time.Sleep(time.Millisecond)

					return nil
				}
			}}
		},
		call:         slowCall,
		wantKind:     outcomeUnknown,
		wantAttempts: 1,
		wantErrs:     []error{ErrCallUnfinished},
		wantTrace:    []string{slowMark},
	}
}

func pipelineLateStartRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: middleware returns nil before its goroutine reaches next ; " +
			"Should: fail with ErrCallNotRun and never run the action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(next Handler) Handler {
				return func(ctx context.Context) error {
					lateStart(ctx, next)

					return nil
				}
			}}
		},
		call:         slowCall,
		wantKind:     outcomeFailure,
		wantAttempts: 1,
		wantErrs:     []error{ErrCallNotRun},
	}
}

func pipelineLateSecondCallRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: middleware runs next, then starts it again in a goroutine and returns ; " +
			"Should: succeed with the first copy and never run the second action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(next Handler) Handler {
				return func(ctx context.Context) error {
					err := next(ctx)

					lateStart(ctx, next)

					return err
				}
			}}
		},
		call:         slowCall,
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{A: 1},
		wantTrace:    []string{slowMark},
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
	wantErrs     []error
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

func pipelineSwallowRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: a middleware swallows the call error ; Should: end the attempt with that call error",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(next Handler) Handler {
				return func(ctx context.Context) error { return swallow(next(ctx)) }
			}}
		},
		call:         failWith(errBoom),
		wantKind:     outcomeFailure,
		wantAttempts: 3,
		wantErrs:     []error{errBoom},
	}
}

func pipelineStaleCopyRow() pipelineRow {
	spec := actionSpec()
	spec.retry.Attempts = 2

	return pipelineRow{
		name: "[Fail] When: attempt 1 succeeds but fails in middleware and attempt 2 is swallowed ; " +
			"Should: fail with attempt 2's call error and no state",
		spec: spec,
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(next Handler) Handler {
				return func(ctx context.Context) error {
					err := next(ctx)
					if attemptOf(ctx) == 1 {
						return errors.Join(err, errBoom)
					}

					return swallow(err)
				}
			}}
		},
		call: func(*recorder) func(context.Context, *pipeState) error {
			return func(ctx context.Context, s *pipeState) error {
				if attemptOf(ctx) == 1 {
					s.B = "stale"

					return nil
				}

				return errCall
			}
		},
		wantKind:     outcomeFailure,
		wantAttempts: 2,
		wantErrs:     []error{errCall},
	}
}

func twiceMiddleware(final func(first, second error) error) func(*recorder) []Middleware {
	return func(*recorder) []Middleware {
		return []Middleware{func(next Handler) Handler {
			return func(ctx context.Context) error { return final(next(ctx), next(ctx)) }
		}}
	}
}

func pipelineSecondCallWinsRow() pipelineRow {
	return pipelineRow{
		name:       "[Success] When: next runs twice and the first call fails ; Should: succeed with the second copy",
		spec:       actionSpec(),
		middleware: twiceMiddleware(lastErr),
		call: func(*recorder) func(context.Context, *pipeState) error {
			n := 0

			return func(_ context.Context, s *pipeState) error {
				n++
				if n == 1 {
					s.B = "first"

					return errBoom
				}

				s.A = n

				return nil
			}
		},
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{A: 2},
	}
}

func pipelineLastCallFailsRow() pipelineRow {
	spec := actionSpec()
	spec.retry.Attempts = 1

	return pipelineRow{
		name: "[Fail] When: next runs twice, the second call fails and middleware returns nil ; " +
			"Should: fail with the second call's error",
		spec: spec,
		middleware: twiceMiddleware(func(first, second error) error {
			discard(first)

			return swallow(second)
		}),
		call: func(*recorder) func(context.Context, *pipeState) error {
			n := 0

			return func(_ context.Context, s *pipeState) error {
				n++
				if n == 1 {
					s.A = 1

					return nil
				}

				return errCall
			}
		},
		wantKind:     outcomeFailure,
		wantAttempts: 1,
		wantErrs:     []error{errCall},
	}
}

func pipelineLaterAttemptSkipRow() pipelineRow {
	spec := actionSpec()
	spec.retry.Attempts = 3

	return pipelineRow{
		name: "[Fail] When: middleware runs next on attempt 1 only ; Should: fail on attempt 2 with ErrCallNotRun",
		spec: spec,
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(next Handler) Handler {
				return func(ctx context.Context) error {
					if attemptOf(ctx) == 1 {
						return swallow(next(ctx))
					}

					return nil
				}
			}}
		},
		call:         failWith(errCall),
		wantKind:     outcomeFailure,
		wantAttempts: 2,
		wantErrs:     []error{ErrCallNotRun},
	}
}

func pipelineLostContextRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: a middleware passes on a context without the call state ; " +
			"Should: not run the call and fail with ErrCallContextLost",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{func(next Handler) Handler {
				return func(context.Context) error { return next(context.Background()) }
			}}
		},
		call:         recordCall,
		wantKind:     outcomeFailure,
		wantAttempts: 1,
		wantErrs:     []error{ErrCallContextLost},
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
		wantErrs:     []error{ErrInvalidState},
	}
}

func baseFor(t *testing.T, row *pipelineRow) snapshot[pipeState] {
	t.Helper()

	if row.corruptBase {
		st, err := newStore(badState{A: "abc", B: "abc", C: "abc"})
		require.NoError(t, err)

		return snapshot[pipeState](st.snapshot())
	}

	return baseOf(t, row.base)
}

func assertCallErr(t *testing.T, got error, want []error) {
	t.Helper()

	if len(want) == 0 {
		require.NoError(t, got)

		return
	}

	for _, w := range want {
		require.ErrorIs(t, got, w)
	}
}

type requireRanRow struct {
	name      string
	noState   bool
	next      func(reached *bool) Handler
	wantErr   error
	wantReach bool
}

func reaching(reached *bool, h Handler) Handler {
	return func(ctx context.Context) error {
		*reached = true

		return h(ctx)
	}
}

func requireRanRows() []requireRanRow {
	run := func(err error) Handler {
		return runCall(func(context.Context, *pipeState) error { return err })
	}

	return []requireRanRow{
		{
			name:    "[Fail] When: the context has no call state ; Should: fail with ErrCallContextLost, no next",
			noState: true,
			next: func(reached *bool) Handler {
				return reaching(reached, func(context.Context) error { return nil })
			},
			wantErr: ErrCallContextLost,
		},
		{
			name: "[Fail] When: the wrapped handler returns its own error ; Should: return it unchanged",
			next: func(reached *bool) Handler {
				return reaching(reached, func(context.Context) error { return errBoom })
			},
			wantErr:   errBoom,
			wantReach: true,
		},
		{
			name: "[Fail] When: the wrapped handler returns nil and no call ran ; Should: fail with ErrCallNotRun",
			next: func(reached *bool) Handler {
				return reaching(reached, func(context.Context) error { return nil })
			},
			wantErr:   ErrCallNotRun,
			wantReach: true,
		},
		{
			name: "[Fail] When: the last call failed and next returns nil ; Should: return the call error",
			next: func(reached *bool) Handler {
				return reaching(reached, func(ctx context.Context) error { return swallow(run(errCall)(ctx)) })
			},
			wantErr:   errCall,
			wantReach: true,
		},
		{
			name: "[Success] When: the last call succeeded ; Should: return nil",
			next: func(reached *bool) Handler {
				return reaching(reached, run(nil))
			},
			wantReach: true,
		},
	}
}

func Test_requireRan(t *testing.T) {
	t.Parallel()

	for _, tt := range requireRanRows() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			if !tt.noState {
				ctx = withCallState(ctx, &callState[pipeState]{base: baseOf(t, pipeState{})})
			}

			reached := false
			err := requireRan[pipeState](tt.next(&reached))(ctx)

			assert.Equal(t, tt.wantReach, reached)

			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.wantErr)
		})
	}
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
		pipelineMiddlewareSeesPanicRow(),
		pipelineNotRunRow(),
		pipelineTwiceRow(),
		pipelineCorruptBaseRow(),
		pipelineSwallowRow(),
		pipelineStaleCopyRow(),
		pipelineSecondCallWinsRow(),
		pipelineLastCallFailsRow(),
		pipelineLostContextRow(),
		pipelineLaterAttemptSkipRow(),
		pipelineAbandonedRow(),
		pipelineAbandonedThenSkipRow(),
		pipelineUnfinishedRow(),
		pipelineLateStartRow(),
		pipelineLateSecondCallRow(),
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

				time.Sleep(time.Minute)

				assert.Equal(t, tt.wantKind, got.kind)
				assert.Equal(t, tt.wantAttempts, got.attempts)
				assert.Equal(t, tt.wantState, got.state)
				assert.Equal(t, tt.wantTrace, rec.snapshot())

				if tt.wantPanic {
					require.ErrorAs(t, got.err, new(*PanicError))

					return
				}

				assertCallErr(t, got.err, tt.wantErrs)
			})
		})
	}
}

func Benchmark_pipeline(b *testing.B) {
	benches := []struct {
		name    string
		timeout time.Duration
	}{
		{name: "no-timeout", timeout: 0},
		{name: "timeout", timeout: time.Second},
	}

	for _, bb := range benches {
		b.Run(bb.name, func(b *testing.B) {
			spec := callSpec{
				saga: "s", step: "pay", phase: PhaseAction, timeout: bb.timeout, retry: RetryPolicy{Attempts: 1},
			}
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
		})
	}
}
