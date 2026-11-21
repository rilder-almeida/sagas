package sagas

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
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
		call      Call
		wantBoom  bool
		wantPanic bool
		wantText  string
	}{
		{
			name: "[Success] When: the call returns nil ; Should: return nil",
			call: func(context.Context) error { return nil },
		},
		{
			name:     "[Fail] When: the call returns an error ; Should: pass the same error through",
			call:     func(context.Context) error { return errBoom },
			wantBoom: true,
			wantText: errBoom.Error(),
		},
		{
			name:      "[Fail] When: the call panics with a string ; Should: return a PanicError with the value",
			call:      func(context.Context) error { panic("bad") },
			wantPanic: true,
			wantText:  "sagas: panic: bad",
		},
		{
			name:      "[Fail] When: the call panics with an error ; Should: return a PanicError with the value",
			call:      func(context.Context) error { panic(errBoom) },
			wantBoom:  true,
			wantPanic: true,
			wantText:  "sagas: panic: boom",
		},
		{
			name: "[Fail] When: the call panics with nil ; Should: still return a PanicError",
			call: func(context.Context) error {
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

			err := recoverer(&callMeta{}).Wrap(t.Context(), CallInfo{}, tt.call)

			panicked, isPanic := errors.AsType[*PanicError](err)

			assert.Equal(t, tt.wantText, errText(err))
			assert.Equal(t, tt.wantBoom, errors.Is(err, errBoom))
			assert.Equal(t, tt.wantPanic, isPanic)
			assert.Equal(t, tt.wantPanic, isPanic && len(panicked.Stack) > 0 && panicked.Value != nil)
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
			name: "[Fail] When: a permanent error wraps ErrCallConcurrent ; Should: be a failure",
			err:  Permanent(ErrCallConcurrent),
			want: outcomeFailure,
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

func Test_PanicError_Unwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		want  error
	}{
		{
			name:  "[Success] When: the value is an error ; Should: return it",
			value: errBoom,
			want:  errBoom,
		},
		{
			name:  "[Success] When: the value is a package Kind ; Should: return it",
			value: ErrConflict,
			want:  ErrConflict,
		},
		{
			name:  "[Success] When: the value is a string ; Should: return nothing",
			value: "text",
		},
		{
			name:  "[Success] When: the value is nil ; Should: return nothing",
			value: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pe := &PanicError{Value: tt.value}

			assert.Equal(t, tt.want, pe.Unwrap())
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

func baseOf[S any](tb testing.TB, s S) snapshot[S] {
	tb.Helper()

	st, err := newStore(s)
	require.NoError(tb, err)

	return st.snapshot()
}

func tagged(r *recorder, name string) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		r.add(name + ">")

		err := call(ctx)

		r.add("<" + name)

		return err
	}
}

func probe(r *recorder) MiddlewareFunc {
	return func(ctx context.Context, info CallInfo, call Call) error {
		if info.IdempotencyKey() == infoExecution+"/"+payStep {
			r.add("info")
		}

		if _, ok := ctx.Deadline(); ok {
			r.add("deadline")
		}

		return call(ctx)
	}
}

func recordCall(r *recorder) func(context.Context, *pipeState) error {
	return func(context.Context, *pipeState) error {
		r.add(callMark)

		return nil
	}
}

func failWith(err error) func(*recorder) func(context.Context, *pipeState) error {
	return func(*recorder) func(context.Context, *pipeState) error {
		return func(context.Context, *pipeState) error { return err }
	}
}

func counter() func() int {
	var n atomic.Int64

	return func() int { return int(n.Add(1)) }
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
		wantTrace:    []string{"A>", "info", "deadline", "B>", callMark, "<B", "<A"},
	}
}

func pipelineUserPanicRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: a user middleware panics ; Should: return a PanicError as unknown without a retry",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(context.Context, CallInfo, Call) error { panic("bad middleware") })}
		},
		call:         recordCall,
		wantKind:     outcomeUnknown,
		wantLabel:    ErrPanic,
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
			count := counter()

			return func(_ context.Context, s *pipeState) error {
				if count() == 1 {
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
		wantCause:    []error{context.DeadlineExceeded},
		wantCauseTxt: `"pay"`,
		wantPrevious: []error{context.DeadlineExceeded, context.DeadlineExceeded},
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
		wantPrevious: []error{errBoom, errBoom},
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

func perAttempt(errs ...error) func(*recorder) func(context.Context, *pipeState) error {
	return func(*recorder) func(context.Context, *pipeState) error {
		count := counter()

		return func(context.Context, *pipeState) error { return errs[count()-1] }
	}
}

func pipelineHistoryRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: three attempts fail with different errors ; Should: return the last, keep the first two " +
			"oldest first",
		spec:         actionSpec(),
		call:         perAttempt(errFirst, errSecond, errThird),
		wantKind:     outcomeFailure,
		wantAttempts: 3,
		wantErrs:     []error{errThird},
		wantPrevious: []error{errFirst, errSecond},
	}
}

func replacing(ctx context.Context, _ CallInfo, call Call) error {
	if err := call(ctx); err != nil {
		return errReplaced
	}

	return nil
}

func pipelineReplacedRow() pipelineRow {
	spec := actionSpec()
	spec.retry.Attempts = 1

	return pipelineRow{
		name: "[Fail] When: middleware replaces the call error ; Should: keep the call error in the history",
		spec: spec,
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(replacing)}
		},
		call:         failWith(errBoom),
		wantKind:     outcomeFailure,
		wantAttempts: 1,
		wantErrs:     []error{errReplaced},
		wantPrevious: []error{errBoom},
	}
}

func pipelineStoppedBackoffRow() pipelineRow {
	spec := actionSpec()
	spec.retry.Initial = 10 * time.Millisecond

	return pipelineRow{
		name: "[Fail] When: the context ends during the wait between attempts ; Should: keep the attempt a failure " +
			"and hold the context cause",
		spec:         spec,
		call:         perAttempt(errFirst, errSecond, errThird),
		cancelAfter:  5 * time.Millisecond,
		cancelCause:  errStop,
		wantKind:     outcomeFailure,
		wantAttempts: 1,
		wantErrs:     []error{errFirst},
		wantCause:    []error{errStop},
	}
}

func pipelineCancelledCallRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: the context ends while the call runs ; Should: end unknown and hold the context cause",
		spec: actionSpec(),
		call: func(*recorder) func(context.Context, *pipeState) error {
			return func(ctx context.Context, _ *pipeState) error {
				<-ctx.Done()

				return ctx.Err()
			}
		},
		cancelAfter:  5 * time.Millisecond,
		cancelCause:  errStop,
		wantKind:     outcomeUnknown,
		wantAttempts: 1,
		wantErrs:     []error{context.Canceled},
		wantCause:    []error{errStop},
	}
}

func pipelineCancelledCompensationRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: the context ends in a compensation with no timeout ; Should: hold the cause",
		spec: compensationSpec(),
		call: func(*recorder) func(context.Context, *pipeState) error {
			return func(ctx context.Context, _ *pipeState) error {
				<-ctx.Done()

				return ctx.Err()
			}
		},
		cancelAfter:  5 * time.Millisecond,
		cancelCause:  errStop,
		wantKind:     outcomeUnknown,
		wantAttempts: 1,
		wantErrs:     []error{context.Canceled},
		wantCause:    []error{errStop},
	}
}

func pipelinePanicValueRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: the call panics with an error value ; Should: match that value through the PanicError",
		spec: actionSpec(),
		call: func(*recorder) func(context.Context, *pipeState) error {
			return func(context.Context, *pipeState) error { panic(errBoom) }
		},
		wantKind:     outcomeUnknown,
		wantLabel:    ErrPanic,
		wantAttempts: 1,
		wantPanic:    true,
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
		wantLabel:    ErrPanic,
		wantAttempts: 1,
		wantPanic:    true,
	}
}

func observePanic(r *recorder) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		err := call(ctx)

		if panicked, ok := errors.AsType[*PanicError](err); ok {
			r.add(fmt.Sprint("panic:", panicked.Value))
		}

		return err
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
		wantLabel:    ErrPanic,
		wantAttempts: 1,
		wantPanic:    true,
		wantTrace:    []string{"panic:bad call"},
	}
}

func pipelineNotRunRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: a middleware returns nil without running the call ; Should: fail with ErrCallNotRun",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(context.Context, CallInfo, Call) error { return nil })}
		},
		call:         recordCall,
		wantKind:     outcomeFailure,
		wantLabel:    ErrCallNotRun,
		wantAttempts: 1,
		wantErrs:     []error{ErrCallNotRun},
	}
}

func pipelineTwiceRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: a middleware runs the call twice ; Should: run the call twice in one attempt",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(ctx context.Context, _ CallInfo, call Call) error {
				return errors.Join(call(ctx), call(ctx))
			})}
		},
		call:         recordCall,
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{},
		wantTrace:    []string{callMark, callMark},
	}
}

func abandoning(start, limit time.Duration) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		errc := make(chan error, 1)

		go func() {
			time.Sleep(start)

			errc <- call(ctx)
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

const (
	slowMark = "slow call"
	callMark = "call"
	runMark  = "run"
)

func slowCall(r *recorder) func(context.Context, *pipeState) error {
	count := counter()

	return func(_ context.Context, s *pipeState) error {
		time.Sleep(20 * time.Millisecond)

		s.A = count()

		r.add(slowMark)

		return nil
	}
}

func lateStart(ctx context.Context, call Call) {
	go func() {
		time.Sleep(time.Millisecond)

		discard(call(ctx))
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
		wantLabel:    ErrCallUnfinished,
		wantAttempts: 1,
		wantErrs:     []error{context.DeadlineExceeded, ErrCallUnfinished},
		wantTrace:    []string{slowMark},
	}
}

func pipelineAbandonedThenSkipRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: attempt 1's goroutine runs the call after a timeout and attempt 2 returns nil " +
			"without the call ; Should: fail with ErrCallNotRun and run no action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			first := abandoning(15*time.Millisecond, 10*time.Millisecond)

			return []Middleware{MiddlewareFunc(func(ctx context.Context, info CallInfo, call Call) error {
				if info.Attempt == 1 {
					return first.Wrap(ctx, info, call)
				}

				time.Sleep(30 * time.Millisecond)

				return nil
			})}
		},
		call:         slowCall,
		wantKind:     outcomeFailure,
		wantLabel:    ErrCallNotRun,
		wantAttempts: 2,
		wantErrs:     []error{ErrCallNotRun},
		wantPrevious: []error{context.DeadlineExceeded},
	}
}

func pipelineUnfinishedRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: middleware starts the call in a goroutine and returns nil while the call runs ; " +
			"Should: end unknown with ErrCallUnfinished after 1 attempt",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(ctx context.Context, _ CallInfo, call Call) error {
				go func() { discard(call(ctx)) }()

				time.Sleep(time.Millisecond)

				return nil
			})}
		},
		call:         slowCall,
		wantKind:     outcomeUnknown,
		wantLabel:    ErrCallUnfinished,
		wantAttempts: 1,
		wantErrs:     []error{ErrCallUnfinished},
		wantTrace:    []string{slowMark},
	}
}

func pipelineLateStartRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: middleware returns nil before its goroutine runs the call ; " +
			"Should: fail with ErrCallNotRun and never run the action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(ctx context.Context, _ CallInfo, call Call) error {
				lateStart(ctx, call)

				return nil
			})}
		},
		call:         slowCall,
		wantKind:     outcomeFailure,
		wantLabel:    ErrCallNotRun,
		wantAttempts: 1,
		wantErrs:     []error{ErrCallNotRun},
	}
}

func pipelineLateSecondCallRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: middleware runs the call, then starts it again in a goroutine and returns ; " +
			"Should: succeed with the first copy and never run the second action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(ctx context.Context, _ CallInfo, call Call) error {
				err := call(ctx)

				lateStart(ctx, call)

				return err
			})}
		},
		call:         slowCall,
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{A: 1},
		wantTrace:    []string{slowMark},
	}
}

const hedgeMark = "hedge fail"

func hedging(ctx context.Context, _ CallInfo, call Call) error {
	replies := make(chan error, 2)

	for range 2 {
		go func() { replies <- call(ctx) }()
	}

	return <-replies
}

func hedgedCall(r *recorder) func(context.Context, *pipeState) error {
	return func(context.Context, *pipeState) error {
		time.Sleep(time.Millisecond)
		r.add(hedgeMark)

		return errBoom
	}
}

func pipelineHedgedRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: middleware runs the call twice at once and returns the first reply ; " +
			"Should: refuse the second call and end unknown after 1 attempt with one action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(hedging)}
		},
		call:         hedgedCall,
		wantKind:     outcomeUnknown,
		wantLabel:    ErrCallUnfinished,
		wantAttempts: 1,
		wantErrs:     []error{ErrCallConcurrent, ErrCallUnfinished},
		wantTrace:    []string{hedgeMark},
	}
}

func waitingBoth(ctx context.Context, _ CallInfo, call Call) error {
	replies := make(chan error, 2)

	for range 2 {
		go func() { replies <- call(ctx) }()
	}

	discard(<-replies)

	return swallow(<-replies)
}

func pipelineConcurrentWaitRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: middleware runs the call twice at once and waits for both ; " +
			"Should: refuse the second call and succeed with the copy of the one action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(waitingBoth)}
		},
		call:         slowCall,
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{A: 1},
		wantTrace:    []string{slowMark},
	}
}

func pipelinePanicAfterLateStartRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: middleware starts a late call in a goroutine and then panics ; " +
			"Should: return a PanicError as unknown and never run the action",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(ctx context.Context, _ CallInfo, call Call) error {
				lateStart(ctx, call)

				panic("bad middleware")
			})}
		},
		call:         slowCall,
		wantKind:     outcomeUnknown,
		wantLabel:    ErrPanic,
		wantAttempts: 1,
		wantPanic:    true,
	}
}

type pipelineRow struct {
	name         string
	spec         callSpec
	base         pipeState
	corruptBase  bool
	middleware   func(r *recorder) []Middleware
	wrap         func(r *recorder) Middleware
	call         func(r *recorder) func(context.Context, *pipeState) error
	wantKind     outcomeKind
	wantAttempts int
	wantErrs     []error
	wantPanic    bool
	wantState    *pipeState
	wantTrace    []string
	wantCause    []error
	wantCauseTxt string
	wantPrevious []error
	wantLabel    *KindError
	cancelAfter  time.Duration
	cancelCause  error
}

func actionSpec() callSpec {
	return callSpec{
		saga: "s", step: payStep, phase: PhaseExecute, timeout: time.Second,
		retry: RetryPolicy{Attempts: 3, Initial: time.Millisecond, NoJitter: true},
	}
}

func compensationSpec() callSpec {
	spec := actionSpec()
	spec.phase = PhaseCompensate

	return spec
}

func pipelineSwallowRow() pipelineRow {
	return pipelineRow{
		name: "[Fail] When: a middleware swallows the call error ; Should: end the attempt with that call error",
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(ctx context.Context, _ CallInfo, call Call) error {
				return swallow(call(ctx))
			})}
		},
		call:         failWith(errBoom),
		wantKind:     outcomeFailure,
		wantAttempts: 3,
		wantErrs:     []error{errBoom},
		wantPrevious: []error{errBoom, errBoom},
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
			return []Middleware{MiddlewareFunc(func(ctx context.Context, info CallInfo, call Call) error {
				err := call(ctx)
				if info.Attempt == 1 {
					return errors.Join(err, errBoom)
				}

				return swallow(err)
			})}
		},
		call: func(*recorder) func(context.Context, *pipeState) error {
			count := counter()

			return func(_ context.Context, s *pipeState) error {
				if count() == 1 {
					s.B = "stale"

					return nil
				}

				return errCall
			}
		},
		wantKind:     outcomeFailure,
		wantAttempts: 2,
		wantErrs:     []error{errCall},
		wantPrevious: []error{errBoom},
	}
}

func twiceMiddleware(final func(first, second error) error) func(*recorder) []Middleware {
	return func(*recorder) []Middleware {
		return []Middleware{MiddlewareFunc(func(ctx context.Context, _ CallInfo, call Call) error {
			return final(call(ctx), call(ctx))
		})}
	}
}

func pipelineSecondCallWinsRow() pipelineRow {
	return pipelineRow{
		name:       "[Success] When: the call runs twice and the first run fails ; Should: succeed with the second one",
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
		name: "[Fail] When: the call runs twice, the second run fails and middleware returns nil ; " +
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
		name: "[Fail] When: middleware runs the call on attempt 1 only ; Should: fail on attempt 2 with ErrCallNotRun",
		spec: spec,
		middleware: func(*recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(ctx context.Context, info CallInfo, call Call) error {
				if info.Attempt == 1 {
					return swallow(call(ctx))
				}

				return nil
			})}
		},
		call:         failWith(errCall),
		wantKind:     outcomeFailure,
		wantLabel:    ErrCallNotRun,
		wantAttempts: 2,
		wantErrs:     []error{ErrCallNotRun},
		wantPrevious: []error{errCall},
	}
}

func markedRow(name string, mws []Middleware, corrupt bool, label *KindError, wantErrs []error) pipelineRow {
	return pipelineRow{
		name: name,
		spec: actionSpec(),
		middleware: func(*recorder) []Middleware {
			return mws
		},
		call:         failWith(errBoom),
		corruptBase:  corrupt,
		wantKind:     outcomeFailure,
		wantLabel:    label,
		wantAttempts: 1,
		wantErrs:     append([]error{errMarker}, wantErrs...),
	}
}

func pipelineMarkedInvalidStateRow() pipelineRow {
	return markedRow(
		"[Fail] When: middleware wraps the decode failure ; Should: keep the marker and the ErrInvalidState label",
		[]Middleware{marking()}, true, ErrInvalidState, []error{ErrInvalidState},
	)
}

func pipelineMarkedPanicRow() pipelineRow {
	row := markedRow(
		"[Fail] When: middleware wraps a panic ; Should: keep the marker and the ErrPanic label",
		[]Middleware{marking()}, false, ErrPanic, []error{errBoom},
	)
	row.call = func(*recorder) func(context.Context, *pipeState) error {
		return func(context.Context, *pipeState) error { panic(errBoom) }
	}
	row.wantKind, row.wantPanic = outcomeUnknown, true

	return row
}

func pipelineCorruptBaseRow() pipelineRow {
	return pipelineRow{
		name:         "[Fail] When: the base cannot be decoded ; Should: fail permanently without running the call",
		spec:         actionSpec(),
		call:         recordCall,
		corruptBase:  true,
		wantKind:     outcomeFailure,
		wantLabel:    ErrInvalidState,
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

func assertCause(t *testing.T, got error, want []error, text string) {
	t.Helper()

	if len(want) == 0 && text == "" {
		require.NoError(t, got)
	}

	for _, w := range want {
		require.ErrorIs(t, got, w)
	}

	if text != "" {
		require.Error(t, got)
		assert.Contains(t, got.Error(), text)
	}
}

func assertPipelineErr(t *testing.T, tt *pipelineRow, got *outcome[pipeState]) {
	t.Helper()

	assert.Same(t, tt.wantLabel, got.label)
	assertCause(t, got.cause, tt.wantCause, tt.wantCauseTxt)
	assertPrevious(t, got.previous, tt.wantPrevious)

	_, isPanic := errors.AsType[*PanicError](got.err)
	assert.Equal(t, tt.wantPanic, isPanic)
	assert.Equal(t, !tt.wantPanic && len(tt.wantErrs) == 0, got.err == nil, "error %v", got.err)

	for _, w := range tt.wantErrs {
		require.ErrorIs(t, got.err, w)
	}
}

func assertPrevious(t *testing.T, got, want []error) {
	t.Helper()

	require.Len(t, got, len(want))

	for i := range want {
		require.ErrorIs(t, got[i], want[i])
	}
}

type requireRanRow struct {
	name      string
	call      func(reached *bool, run func(error) Call) Call
	wantErr   error
	wantReach bool
}

func reaching(reached *bool, call Call) Call {
	return func(ctx context.Context) error {
		*reached = true

		return call(ctx)
	}
}

func requireRanRows() []requireRanRow {
	return []requireRanRow{
		{
			name: "[Fail] When: the wrapped call returns its own error ; Should: return it unchanged",
			call: func(reached *bool, _ func(error) Call) Call {
				return reaching(reached, func(context.Context) error { return errBoom })
			},
			wantErr:   errBoom,
			wantReach: true,
		},
		{
			name: "[Fail] When: the wrapped call returns nil and no call ran ; Should: fail with ErrCallNotRun",
			call: func(reached *bool, _ func(error) Call) Call {
				return reaching(reached, func(context.Context) error { return nil })
			},
			wantErr:   ErrCallNotRun,
			wantReach: true,
		},
		{
			name: "[Fail] When: the last call failed and the wrapped call returns nil ; Should: return the call error",
			call: func(reached *bool, run func(error) Call) Call {
				return reaching(reached, func(ctx context.Context) error { return swallow(run(errCall)(ctx)) })
			},
			wantErr:   errCall,
			wantReach: true,
		},
		{
			name: "[Success] When: the last call succeeded ; Should: return nil",
			call: func(reached *bool, run func(error) Call) Call {
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

			at := &attemptState[pipeState]{base: baseOf(t, pipeState{})}
			run := func(err error) Call {
				return runCall(at, func(context.Context, *pipeState) error { return err }, "key")
			}

			var out *pipeState

			reached := false
			err := requireRan(at, &callMeta{}, &out).Wrap(t.Context(), CallInfo{}, tt.call(&reached, run))

			assert.Equal(t, tt.wantReach, reached)

			if tt.wantErr == nil {
				require.NoError(t, err)
				assert.Equal(t, &pipeState{}, out)

				return
			}

			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, out)
		})
	}
}

func infoText(info *CallInfo) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%s",
		info.Saga, info.Execution, info.Step, info.Phase, info.Attempt, info.IdempotencyKey())
}

func keyCall(r *recorder) func(context.Context, *pipeState) error {
	return func(ctx context.Context, _ *pipeState) error {
		r.add("step:" + IdempotencyKey(ctx))

		return nil
	}
}

func pipelineBackgroundRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: middleware calls the call with context.Background ; " +
			"Should: give the step its key and the middleware every CallInfo field",
		spec: actionSpec(),
		middleware: func(r *recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(_ context.Context, info CallInfo, call Call) error {
				r.add(infoText(&info))

				return call(context.Background())
			})}
		},
		call:         keyCall,
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{},
		wantTrace:    []string{"s|exec-1|pay|execute|1|exec-1/pay", "step:exec-1/pay"},
	}
}

func infoFieldRow(field string, pick func(CallInfo) string, want ...string) pipelineRow {
	key := "step:" + infoExecution + "/pay"

	return pipelineRow{
		name: "[Success] When: a middleware reads the CallInfo field " + field + " over three attempts ; " +
			"Should: see it set and the step read the same key",
		spec: actionSpec(),
		middleware: func(r *recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(ctx context.Context, info CallInfo, call Call) error {
				r.add(pick(info))
				r.add("key:" + info.IdempotencyKey())

				return call(ctx)
			})}
		},
		call: func(r *recorder) func(context.Context, *pipeState) error {
			count := counter()

			return func(ctx context.Context, _ *pipeState) error {
				r.add("step:" + IdempotencyKey(ctx))

				if count() < 3 {
					return errBoom
				}

				return nil
			}
		},
		wantKind:     outcomeSuccess,
		wantAttempts: 3,
		wantState:    &pipeState{},
		wantTrace: []string{
			want[0], "key:" + infoExecution + "/pay", key,
			want[1], "key:" + infoExecution + "/pay", key,
			want[2], "key:" + infoExecution + "/pay", key,
		},
	}
}

func pipelineInfoFieldRows() []pipelineRow {
	return []pipelineRow{
		infoFieldRow("Saga", func(i CallInfo) string { return i.Saga }, "s", "s", "s"),
		infoFieldRow("Execution", func(i CallInfo) string { return i.Execution }, "exec-1", "exec-1", "exec-1"),
		infoFieldRow("Step", func(i CallInfo) string { return i.Step }, "pay", "pay", "pay"),
		infoFieldRow("Phase", func(i CallInfo) string { return string(i.Phase) }, "execute", "execute", "execute"),
		infoFieldRow("Attempt", func(i CallInfo) string { return strconv.Itoa(i.Attempt) }, "1", "2", "3"),
		infoFieldRow("IdempotencyKey", func(i CallInfo) string { return i.IdempotencyKey() },
			"exec-1/pay", "exec-1/pay", "exec-1/pay"),
	}
}

func pipelineStepWrapRow() pipelineRow {
	return pipelineRow{
		name: "[Success] When: a step wrap and two saga middlewares wrap the call ; " +
			"Should: run the saga middleware outermost, then the step wrap, then the call",
		spec: actionSpec(),
		middleware: func(r *recorder) []Middleware {
			return []Middleware{tagged(r, "A"), tagged(r, "B")}
		},
		wrap: func(r *recorder) Middleware {
			return tagged(r, "step")
		},
		call:         recordCall,
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{},
		wantTrace:    []string{"A>", "B>", "step>", callMark, "<step", "<B", "<A"},
	}
}

func pipelinePhaseRow(phase Phase, wantTrace ...string) pipelineRow {
	spec := actionSpec()
	spec.phase = phase

	return pipelineRow{
		name: "[Success] When: the call runs in phase " + string(phase) + " ; " +
			"Should: give the middleware that phase and a deadline only outside a compensation",
		spec: spec,
		middleware: func(r *recorder) []Middleware {
			return []Middleware{MiddlewareFunc(func(ctx context.Context, info CallInfo, call Call) error {
				r.add(string(info.Phase))

				if _, ok := ctx.Deadline(); ok {
					r.add("deadline")
				}

				return call(ctx)
			})}
		},
		call:         recordCall,
		wantKind:     outcomeSuccess,
		wantAttempts: 1,
		wantState:    &pipeState{},
		wantTrace:    wantTrace,
	}
}

func pipelinePhaseRows() []pipelineRow {
	return []pipelineRow{
		pipelinePhaseRow(PhaseExecute, "execute", "deadline", callMark),
		pipelinePhaseRow(PhasePivot, "pivot", "deadline", callMark),
		pipelinePhaseRow(PhaseCompensate, "compensate", callMark),
	}
}

func Test_pipeline(t *testing.T) {
	t.Parallel()

	tests := slices.Concat(pipelineInfoFieldRows(), pipelinePhaseRows(), []pipelineRow{
		pipelineBackgroundRow(),
		pipelineStepWrapRow(),
		pipelineOrderRow(),
		pipelineUserPanicRow(),
		pipelineFreshCopyRow(),
		pipelineTimeoutRow(),
		pipelineHistoryRow(),
		pipelineReplacedRow(),
		pipelineStoppedBackoffRow(),
		pipelineCancelledCallRow(),
		pipelineCancelledCompensationRow(),
		pipelinePanicValueRow(),
		pipelineMarkedInvalidStateRow(),
		pipelineMarkedPanicRow(),
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
		pipelineLaterAttemptSkipRow(),
		pipelineAbandonedRow(),
		pipelineAbandonedThenSkipRow(),
		pipelineUnfinishedRow(),
		pipelineLateStartRow(),
		pipelineLateSecondCallRow(),
		pipelineHedgedRow(),
		pipelineConcurrentWaitRow(),
		pipelinePanicAfterLateStartRow(),
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				rec := &recorder{}
				spec := tt.spec

				if tt.middleware != nil {
					spec.middleware = tt.middleware(rec)
				}

				if tt.wrap != nil {
					spec.wrap = tt.wrap(rec)
				}

				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)

				if tt.cancelCause != nil {
					time.AfterFunc(tt.cancelAfter, func() { cancel(tt.cancelCause) })
				}

				got := pipeline(&spec, tt.call(rec))(ctx, &callMeta{execution: infoExecution}, baseFor(t, &tt))

				time.Sleep(time.Minute)

				assert.Equal(t, tt.wantKind, got.kind)
				assert.Equal(t, tt.wantAttempts, got.attempts)
				assert.Equal(t, tt.wantState, got.state)
				assert.Equal(t, tt.wantTrace, rec.snapshot())

				assertPipelineErr(t, &tt, &got)
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
				saga: "s", step: "pay", phase: PhaseExecute, timeout: bb.timeout, retry: RetryPolicy{Attempts: 1},
			}
			call := pipeline(&spec, func(_ context.Context, s *pipeState) error {
				s.A++

				return nil
			})
			base := baseOf(b, pipeState{A: 1, B: "x"})
			ctx := context.Background()

			b.ReportAllocs()

			for b.Loop() {
				if got := call(ctx, &callMeta{execution: infoExecution}, base); got.kind != outcomeSuccess {
					b.Fatal(got.err)
				}
			}
		})
	}
}

func Test_MiddlewareFunc_Wrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		info    CallInfo
		callErr error
		err     error
	}{
		{
			name: "[Success] When: the function returns nil ; Should: pass ctx, info and call and return nil",
			info: CallInfo{
				Saga:      "s",
				Execution: "e",
				Step:      payStep,
				Phase:     PhaseExecute,
				Attempt:   1,
			},
			callErr: nil,
			err:     nil,
		},
		{
			name:    "[Fail] When: the function returns an error ; Should: return the same error",
			info:    CallInfo{Step: "undo", Phase: PhaseCompensate, Attempt: 2},
			callErr: errCall,
			err:     errBoom,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			type ctxKey struct{}

			ctx := context.WithValue(t.Context(), ctxKey{}, tt.name)
			called := false
			call := func(context.Context) error {
				called = true

				return tt.callErr
			}

			var (
				gotValue   any
				gotInfo    CallInfo
				gotCallErr error
			)

			mw := MiddlewareFunc(func(ctx context.Context, info CallInfo, call Call) error {
				gotValue, gotInfo, gotCallErr = ctx.Value(ctxKey{}), info, call(ctx)

				return tt.err
			})

			err := mw.Wrap(ctx, tt.info, call)

			assert.Equal(t, tt.err, err)
			assert.Equal(t, tt.name, gotValue)
			assert.Equal(t, tt.info, gotInfo)
			assert.True(t, called)
			assert.Equal(t, tt.callErr, gotCallErr)
		})
	}
}

type tagMiddleware struct {
	r    *recorder
	name string
}

func (m tagMiddleware) Wrap(ctx context.Context, _ CallInfo, call Call) error {
	m.r.add(m.name + ">")

	err := call(ctx)

	m.r.add("<" + m.name)

	return err
}

func Test_Chain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mws       func(r *recorder) []Middleware
		overwrite bool
		wantPanic string
		wantTrace []string
		wantErr   error
	}{
		{
			name:      "[Success] When: no middleware is given ; Should: run the call alone",
			mws:       func(*recorder) []Middleware { return nil },
			wantTrace: []string{runMark},
		},
		{
			name: "[Success] When: the caller overwrites its slice after Chain ; Should: run the original middlewares",
			mws: func(r *recorder) []Middleware {
				return []Middleware{tagMiddleware{r, "a"}, tagMiddleware{r, "b"}}
			},
			overwrite: true,
			wantTrace: []string{"a>", "b>", runMark, "<b", "<a"},
		},
		{
			name:      "[Success] When: one middleware is given ; Should: wrap the call",
			mws:       func(r *recorder) []Middleware { return []Middleware{tagMiddleware{r, "a"}} },
			wantTrace: []string{"a>", runMark, "<a"},
		},
		{
			name: "[Success] When: three middlewares are given, one a MiddlewareFunc ; Should: run the first outermost",
			mws: func(r *recorder) []Middleware {
				return []Middleware{tagMiddleware{r, "a"}, tagged(r, "b"), tagMiddleware{r, "c"}}
			},
			wantTrace: []string{"a>", "b>", "c>", runMark, "<c", "<b", "<a"},
		},
		{
			name: "[Fail] When: a middleware returns an error without calling the call ; Should: stop the rest",
			mws: func(r *recorder) []Middleware {
				stop := MiddlewareFunc(func(context.Context, CallInfo, Call) error { return errBoom })

				return []Middleware{tagMiddleware{r, "a"}, stop, tagMiddleware{r, "c"}}
			},
			wantTrace: []string{"a>", "<a"},
			wantErr:   errBoom,
		},
		{
			name:      "[Fail] When: an item is nil ; Should: panic naming its position",
			mws:       func(r *recorder) []Middleware { return []Middleware{tagMiddleware{r, "a"}, nil} },
			wantPanic: "sagas: Chain item 1 is nil",
		},
		{
			name: "[Fail] When: an item is a nil value with a type ; Should: panic naming its position",
			mws: func(r *recorder) []Middleware {
				return []Middleware{(*tagMiddleware)(nil), tagMiddleware{r, "b"}}
			},
			wantPanic: "sagas: Chain item 0 is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recorder{}
			mws := tt.mws(rec)

			var (
				chain MiddlewareFunc
				err   error
			)

			build := func() { chain = Chain(mws...) }

			if tt.wantPanic != "" {
				require.PanicsWithValue(t, tt.wantPanic, build)
			} else {
				require.NotPanics(t, build)

				if tt.overwrite {
					mws[0] = tagged(rec, "changed")
				}

				err = chain.Wrap(t.Context(), CallInfo{}, func(context.Context) error {
					rec.add(runMark)

					return nil
				})
			}

			assert.Equal(t, tt.wantTrace, rec.snapshot())
			assert.Equal(t, tt.wantErr, err)
		})
	}
}

func Test_recoverPanic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		fn           func() error
		wantPanicked bool
		wantErr      error
	}{
		{
			name: "[Success] When: the function returns nil ; Should: report no panic and no error",
			fn:   func() error { return nil },
		},
		{
			name:    "[Fail] When: the function returns an error ; Should: report no panic and keep the error",
			fn:      func() error { return errBoom },
			wantErr: errBoom,
		},
		{
			name:         "[Fail] When: the function panics with an error ; Should: report a panic wrapping it",
			fn:           func() error { panic(errBoom) },
			wantPanicked: true,
			wantErr:      errBoom,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			panicked, err := recoverPanic(tt.fn)

			assert.Equal(t, tt.wantPanicked, panicked)

			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantPanicked, errors.As(err, new(*PanicError)))
		})
	}
}
