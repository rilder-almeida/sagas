package sagastest

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type defect string

const (
	sound                defect = ""
	refundFailsUnapplied defect = "refund fails unapplied"
	refundTwice          defect = "refund twice"
	chargeIgnoresRefund  defect = "charge ignores refund"
	refundLandedTwice    defect = "refund landed twice"
	shipsTwice           defect = "ships twice"
	pivotsTwice          defect = "pivots twice"
	pivotErrors          defect = "pivot errors when repeated"
	shipErrors           defect = "ship errors when repeated"
	bothTwice            defect = "pivot and ship twice"
)

const (
	price      = 100
	stepCharge = "charge"
	stepShip   = "ship"
	stepPivot  = "pivot"
	stepNotify = "notify"
)

var (
	errNoCharge     = errors.New("refund of a charge that never landed")
	errRefused      = errors.New("late refused")
	errRefundedMore = errors.New("refunded more than once")
	errUnbalanced   = errors.New("balance off")
	errShippedMore  = errors.New("shipped more than once")
	errPivotedMore  = errors.New("pivot applied more than once")
	errPivotAgain   = errors.New("pivot already applied")
	errShipAgain    = errors.New("ship already sent")
	errNoLateAction = errors.New("no late action")
)

const noCallReport = "the clean run recorded no call: Setup must pass mw to sagas.WithMiddleware, " +
	"or every step is disabled in the setup state"

type ledger struct {
	mu      sync.Mutex
	defect  defect
	balance int
	charged map[string]bool
	refunds map[string]int
	shipped map[string]int
	pivoted map[string]int
}

func newLedger(d defect) *ledger {
	return &ledger{
		defect: d, charged: map[string]bool{}, refunds: map[string]int{},
		shipped: map[string]int{}, pivoted: map[string]int{},
	}
}

func (l *ledger) ship(key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.shipped[key] > 0 && l.defect == shipErrors {
		return errShipAgain
	}

	if l.shipped[key] == 0 || l.defect == shipsTwice || l.defect == bothTwice {
		l.shipped[key]++
	}

	return nil
}

func (l *ledger) pivot(key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.pivoted[key] > 0 && l.defect == pivotErrors {
		return errPivotAgain
	}

	if l.pivoted[key] == 0 || l.defect == pivotsTwice || l.defect == bothTwice {
		l.pivoted[key]++
	}

	return nil
}

func (l *ledger) charge(key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.charged[key] || (l.refunds[key] > 0 && l.defect != chargeIgnoresRefund) {
		return nil
	}

	l.charged[key] = true
	l.balance += price

	return nil
}

func (l *ledger) refund(key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	switch {
	case !l.charged[key] && l.defect == refundFailsUnapplied:
		return errNoCharge
	case l.refunds[key] > 0 && l.defect != refundTwice && (l.defect != refundLandedTwice || !l.charged[key]):
		return nil
	}

	l.refunds[key]++
	if l.charged[key] {
		l.balance -= price
	}

	return nil
}

func (l *ledger) check() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var (
		errs []error
		want int
	)

	for key, n := range l.refunds {
		if n > 1 {
			errs = append(errs, fmt.Errorf("%w: key %q refunded %d times", errRefundedMore, key, n))
		}
	}

	for key, n := range l.shipped {
		if n > 1 {
			errs = append(errs, fmt.Errorf("%w: key %q shipped %d times", errShippedMore, key, n))
		}
	}

	for key, n := range l.pivoted {
		if n > 1 {
			errs = append(errs, fmt.Errorf("%w: key %q applied %d times", errPivotedMore, key, n))
		}
	}

	for key, on := range l.charged {
		if on && l.refunds[key] == 0 {
			want += price
		}
	}

	if l.balance != want {
		errs = append(errs, fmt.Errorf("%w: balance %d, want %d", errUnbalanced, l.balance, want))
	}

	return errors.Join(errs...)
}

type chargeStep struct {
	sagas.Step

	book *ledger
}

func newCharge(book *ledger) *chargeStep {
	s := &chargeStep{book: book}
	s.Name, s.Retry = stepCharge, sagas.NoRetry()

	return s
}

func (s *chargeStep) Execute(ctx context.Context, _ *probeState) error {
	return s.book.charge(sagas.IdempotencyKey(ctx))
}

func (s *chargeStep) Compensate(ctx context.Context, _ *probeState) error {
	return s.book.refund(sagas.IdempotencyKey(ctx))
}

type disabledCharge struct {
	chargeStep
}

func newDisabledCharge(book *ledger) *disabledCharge {
	s := &disabledCharge{book: book}
	s.Name, s.Retry = stepCharge, sagas.NoRetry()

	return s
}

func (*disabledCharge) Enabled(*probeState) bool { return false }

type shipStep struct {
	sagas.Step

	err error
}

func newShip(err error) *shipStep {
	s := &shipStep{err: err}
	s.Name, s.Retry = stepShip, sagas.NoRetry()

	return s
}

func (s *shipStep) Execute(context.Context, *probeState) error { return s.err }

type pivotStep struct {
	sagas.Step
}

func newPivotStep() *pivotStep {
	s := &pivotStep{}
	s.Name, s.Retry = stepPivot, sagas.NoRetry()

	return s
}

func (*pivotStep) Pivot(context.Context, *probeState) error { return nil }

type ledgerPivot struct {
	sagas.Step

	book  *ledger
	bump  bool
	calls atomic.Int32
}

func newLedgerPivot(book *ledger, attempts int, bump bool) *ledgerPivot {
	s := &ledgerPivot{book: book, bump: bump}
	s.Name = stepPivot
	s.Retry = sagas.RetryPolicy{Attempts: attempts, Initial: time.Millisecond, NoJitter: true}

	return s
}

func (s *ledgerPivot) Pivot(ctx context.Context, st *probeState) error {
	n := int(s.calls.Add(1))
	if s.bump {
		st.N = n
	}

	return s.book.pivot(sagas.IdempotencyKey(ctx))
}

type ledgerShip struct {
	sagas.Step

	book *ledger
}

func newLedgerShip(book *ledger) *ledgerShip {
	s := &ledgerShip{book: book}
	s.Name, s.Retry = stepShip, sagas.NoRetry()

	return s
}

func (s *ledgerShip) Execute(ctx context.Context, _ *probeState) error {
	return s.book.ship(sagas.IdempotencyKey(ctx))
}

type disabledShip struct {
	ledgerShip
}

func newDisabledShip(book *ledger) *disabledShip {
	s := &disabledShip{}
	s.book = book
	s.Name, s.Retry = stepShip, sagas.NoRetry()

	return s
}

func (*disabledShip) Enabled(*probeState) bool { return false }

type notifyStep struct {
	sagas.Step
}

func newNotify() *notifyStep {
	s := &notifyStep{}
	s.Name, s.Retry = stepNotify, sagas.NoRetry()

	return s
}

func (*notifyStep) Execute(context.Context, *probeState) error { return nil }

func Test_planOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		flow        sagas.Flow
		wantSteps   []string
		wantPivot   string
		wantForward []string
		wantErr     error
	}{
		{
			name: "[Success] When: two forward-only steps run in parallel after the pivot ; " +
				"Should: list both in the forward-only zone",
			flow: sagas.Sequence(
				newCharge(newLedger(sound)), newPivotStep(), sagas.Parallel(newShip(nil), newNotify()),
			),
			wantSteps:   []string{stepCharge, stepPivot, stepShip, stepNotify},
			wantPivot:   stepPivot,
			wantForward: []string{stepShip, stepNotify},
		},
		{
			name: "[Success] When: a step runs two hops after the pivot ; " +
				"Should: list it in the forward-only zone too",
			flow: sagas.Sequence(
				newCharge(newLedger(sound)), newPivotStep(), newShip(nil), newNotify(),
			),
			wantSteps:   []string{stepCharge, stepPivot, stepShip, stepNotify},
			wantPivot:   stepPivot,
			wantForward: []string{stepShip, stepNotify},
		},
		{
			name:      "[Success] When: the flow has no pivot ; Should: give no pivot name and an empty zone",
			flow:      sagas.Sequence(newCharge(newLedger(sound)), newShip(nil)),
			wantSteps: []string{stepCharge, stepShip},
		},
		{
			name:      "[Success] When: a step has Enabled ; Should: still list it among the steps",
			flow:      sagas.Sequence(newDisabledCharge(newLedger(sound)), newShip(nil)),
			wantSteps: []string{stepCharge, stepShip},
		},
		{
			name:    "[Fail] When: a step after the pivot has Compensate ; Should: return the error of Inspect",
			flow:    sagas.Sequence(newPivotStep(), newCharge(newLedger(sound))),
			wantErr: sagas.ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := planOf[probeState](tt.flow)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			names := make([]string, 0, len(p.steps))
			for _, s := range p.steps {
				names = append(names, s.Name)
			}

			assert.Equal(t, tt.wantSteps, names)
			assert.Equal(t, tt.wantPivot, p.pivot)
			assert.Equal(t, tt.wantForward, p.forwardOnly)
		})
	}
}

type lateFunc func(ctx context.Context, step, key string) error

func lateCharge(book *ledger) lateFunc {
	return func(_ context.Context, step, key string) error {
		if step != stepCharge {
			return fmt.Errorf("%w: step %q", errNoLateAction, step)
		}

		return book.charge(key)
	}
}

func lateRefused(*ledger) lateFunc {
	return func(context.Context, string, string) error { return errRefused }
}

func noLate(*ledger) lateFunc { return nil }

func shopSetup(t *testing.T, d defect, flow func(*ledger) *sagas.Group, late func(*ledger) lateFunc) Setup[probeState] {
	t.Helper()

	return newShop(func(err error) { require.NoError(t, err) }, d, flow, late)
}

func newShop(
	abort func(error), d defect, flow func(*ledger) *sagas.Group, late func(*ledger) lateFunc,
) Setup[probeState] {
	return func(mw ...sagas.Middleware) Case[probeState] {
		book := newLedger(d)
		f := flow(book)

		saga, err := sagas.New[probeState]("shop", f, sagas.WithMiddleware(mw...))
		if err != nil {
			abort(err)
			panic(err)
		}

		return Case[probeState]{
			Saga:  saga,
			State: &probeState{},
			Check: func(*probeState) error { return book.check() },
			Flow:  f,
			Late:  late(book),
		}
	}
}

func declaring(setup Setup[probeState], flow sagas.Flow) Setup[probeState] {
	return func(mw ...sagas.Middleware) Case[probeState] {
		c := setup(mw...)
		c.Flow = flow

		return c
	}
}

func chargeThenShip(book *ledger) *sagas.Group { return sagas.Sequence(newCharge(book), newShip(nil)) }

func droppingMiddleware(setup Setup[probeState]) Setup[probeState] {
	return func(...sagas.Middleware) Case[probeState] { return setup() }
}

type fakeTB struct {
	testing.TB

	errors []string
	logs   []string
}

func (*fakeTB) Helper() {}

func (f *fakeTB) Context() context.Context {
	if f.TB == nil {
		return context.Background()
	}

	return f.TB.Context()
}

func (f *fakeTB) Errorf(format string, args ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

func (f *fakeTB) Error(args ...any) {
	f.errors = append(f.errors, fmt.Sprint(args...))
}

func (f *fakeTB) Logf(format string, args ...any) {
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func Test_Compensations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(t *testing.T) Setup[probeState]
		wantErrors []string
		wantCauses []string
		wantLogs   []string
		wantSetups int
	}{
		{
			name: "[Success] When: the ledger keeps tombstones and refunds once ; Should: report no error",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, chargeThenShip, lateCharge)
			},
			wantSetups: 4,
		},
		{
			name: "[Fail] When: the refund fails on a charge that never landed ; " +
				"Should: report the cases where the charge never landed",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, refundFailsUnapplied, chargeThenShip, lateCharge)
			},
			wantErrors: []string{
				`step "charge", case "never applied": Run returned .+, want sagas\.ErrCompensated`,
				`step "charge", case "step lands after its compensation": Run returned .+, want sagas\.ErrCompensated`,
			},
			wantCauses: []string{errNoCharge.Error(), errNoCharge.Error()},
			wantSetups: 4,
		},
		{
			name: "[Fail] When: the refund posts twice and the run still ends compensated ; " +
				"Should: report the Check error of the twice case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, refundTwice, chargeThenShip, lateCharge)
			},
			wantErrors: []string{
				`step "charge", case "compensation runs twice": ` +
					`Check: refunded more than once: key "run-1/charge" refunded 2 times`,
			},
			wantSetups: 4,
		},
		{
			name: "[Fail] When: the refund of a landed charge has no guard against a second refund ; " +
				"Should: report the Check error of the twice case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, refundLandedTwice, chargeThenShip, lateCharge)
			},
			wantErrors: []string{
				`step "charge", case "compensation runs twice": ` +
					`Check: refunded more than once: key "run-1/charge" refunded 2 times`,
			},
			wantSetups: 4,
		},
		{
			name: "[Fail] When: the charge ignores the refund tombstone ; Should: report the late case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, chargeIgnoresRefund, chargeThenShip, lateCharge)
			},
			wantErrors: []string{
				`step "charge", case "step lands after its compensation": Check: balance off: balance 100, want 0`,
			},
			wantSetups: 4,
		},
		{
			name: "[Fail] When: the case has no Late ; Should: report that Late is nil",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, chargeThenShip, noLate)
			},
			wantErrors: []string{`step "charge", case "step lands after its compensation": Case.Late is nil`},
			wantSetups: 4,
		},
		{
			name: "[Success] When: Late returns an error ; Should: log it and let Check decide",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, chargeThenShip, lateRefused)
			},
			wantLogs:   []string{`step "charge", case "step lands after its compensation": Late: late refused`},
			wantSetups: 4,
		},
		{
			name: "[Fail] When: Case.Flow lacks a step the clean run called ; Should: report that step and run no case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return declaring(shopSetup(t, sound, chargeThenShip, lateCharge), newCharge(newLedger(sound)))
			},
			wantErrors: []string{`step "ship" ran in the clean run`},
			wantSetups: 1,
		},
		{
			name: "[Fail] When: Case.Flow is not a valid flow ; Should: report it and run no case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				invalid := sagas.Sequence(newPivotStep(), newCharge(newLedger(sound)))

				return declaring(shopSetup(t, sound, chargeThenShip, lateCharge), invalid)
			},
			wantErrors: []string{"Case.Flow is not a valid flow"},
			wantSetups: 1,
		},
		{
			name: "[Fail] When: a step before the compensable step fails only in the cases ; " +
				"Should: report that the step did not start with the error of Run",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				builds := 0

				return shopSetup(t, sound, func(book *ledger) *sagas.Group {
					builds++

					var err error
					if builds > 1 {
						err = errBoom
					}

					return sagas.Sequence(newShip(err), newCharge(book))
				}, lateCharge)
			},
			wantErrors: []string{
				`step "charge", case "never applied": the step did not start, Run returned`,
			},
			wantCauses: []string{errBoom.Error()},
			wantSetups: 2,
		},
		{
			name: "[Fail] When: the compensable step is disabled ; Should: report that the step did not start",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, func(book *ledger) *sagas.Group {
					return sagas.Sequence(newDisabledCharge(book), newShip(nil))
				}, lateCharge)
			},
			wantErrors: []string{`step "charge", case "never applied": the step did not start`},
			wantSetups: 2,
		},
		{
			name: "[Fail] When: the clean run fails ; Should: report it and run no case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, func(book *ledger) *sagas.Group {
					return sagas.Sequence(newCharge(book), newShip(errBoom))
				}, lateCharge)
			},
			wantErrors: []string{"the clean run failed"},
			wantSetups: 1,
		},
		{
			name: "[Fail] When: no step has Compensate ; Should: report it and run no case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				shipOnly := func(*ledger) *sagas.Group { return sagas.Sequence(newShip(nil)) }

				return shopSetup(t, sound, shipOnly, lateCharge)
			},
			wantErrors: []string{"no step with Compensate"},
			wantSetups: 1,
		},
		{
			name: "[Fail] When: Setup drops mw ; Should: report that the clean run recorded no call and run no case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return droppingMiddleware(shopSetup(t, sound, chargeThenShip, lateCharge))
			},
			wantErrors: []string{regexp.QuoteMeta(noCallReport)},
			wantSetups: 1,
		},
		{
			name: "[Fail] When: Setup passes mw but every step is disabled in the setup state ; " +
				"Should: report that the clean run recorded no call and run no case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, func(book *ledger) *sagas.Group {
					return sagas.Sequence(newDisabledCharge(book), newDisabledShip(book))
				}, lateCharge)
			},
			wantErrors: []string{regexp.QuoteMeta(noCallReport)},
			wantSetups: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			setup := tt.setup(t)
			setups := 0
			counted := func(mw ...sagas.Middleware) Case[probeState] {
				setups++

				return setup(mw...)
			}

			fake := &fakeTB{TB: t}
			Compensations(fake, counted)

			require.Len(t, fake.errors, len(tt.wantErrors), fake.errors)

			for i, want := range tt.wantErrors {
				assert.Regexp(t, want, fake.errors[i])
			}

			require.LessOrEqual(t, len(tt.wantCauses), len(fake.errors))

			for i, cause := range tt.wantCauses {
				require.NotEmpty(t, cause)
				assert.Contains(t, fake.errors[i], cause)
			}

			require.Len(t, fake.logs, len(tt.wantLogs), fake.logs)

			for i, want := range tt.wantLogs {
				assert.Contains(t, fake.logs[i], want)
			}

			assert.Equal(t, tt.wantSetups, setups)
		})
	}
}

func swallowing(setup Setup[probeState]) Setup[probeState] {
	swallow := sagas.MiddlewareFunc(func(ctx context.Context, _ sagas.CallInfo, call sagas.Call) error {
		if err := call(ctx); !errors.Is(err, context.DeadlineExceeded) {
			return err
		}

		return nil
	})

	return func(mw ...sagas.Middleware) Case[probeState] {
		return setup(append([]sagas.Middleware{swallow}, mw...)...)
	}
}

func pivotFlow(attempts int, bump bool) func(*ledger) *sagas.Group {
	return func(book *ledger) *sagas.Group {
		return sagas.Sequence(newCharge(book), newLedgerPivot(book, attempts, bump), newLedgerShip(book))
	}
}

func Test_Pivot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(t *testing.T) Setup[probeState]
		wantErrors []string
		wantCauses []string
		wantSetups int
	}{
		{
			name: "[Success] When: the pivot has three attempts and the forward-only step is idempotent ; " +
				"Should: report no error",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, pivotFlow(3, false), noLate)
			},
			wantSetups: 3,
		},
		{
			name: "[Success] When: the pivot has one attempt ; " +
				"Should: expect an incomplete run with only the pivot pending and report no error",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, pivotFlow(1, false), noLate)
			},
			wantSetups: 3,
		},
		{
			name: "[Success] When: the pivot has zero attempts ; " +
				"Should: read the default attempts and expect the completed path",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, pivotFlow(0, false), noLate)
			},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: the forward-only step ships twice when repeated ; " +
				"Should: report the Check error of the repeated case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, shipsTwice, pivotFlow(3, false), noLate)
			},
			wantErrors: []string{
				`step "ship", case "step repeated": Check: shipped more than once: key "run-1/ship" shipped 2 times`,
			},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: the pivot applies its effect again after the unknown outcome ; " +
				"Should: report the Check error of the pivot case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, pivotsTwice, pivotFlow(3, false), noLate)
			},
			wantErrors: []string{
				`step "pivot", case "unknown outcome": Check: pivot applied more than once: ` +
					`key "run-1/pivot" applied 2 times`,
			},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: the final state differs from the state of the clean run ; " +
				"Should: report the step and both states",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, pivotFlow(3, true), noLate)
			},
			wantErrors: []string{
				`step "pivot", case "unknown outcome": the final state {N:2} differs from the clean run {N:1}`,
			},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: a forward-only step is disabled ; Should: report that the step did not start",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, func(book *ledger) *sagas.Group {
					return sagas.Sequence(newCharge(book), newLedgerPivot(book, 3, false), newDisabledShip(book))
				}, noLate)
			},
			wantErrors: []string{`step "ship", case "step repeated": the step did not start`},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: the pivot returns an error when it is called again with the same key ; " +
				"Should: report the Run error of the pivot case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, pivotErrors, pivotFlow(3, false), noLate)
			},
			wantErrors: []string{`step "pivot", case "unknown outcome": Run returned`},
			wantCauses: []string{"pivot already applied"},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: the forward-only step returns an error when it is called again ; " +
				"Should: report the Run error of the repeated case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, shipErrors, pivotFlow(3, false), noLate)
			},
			wantErrors: []string{`step "ship", case "step repeated": Run returned`},
			wantCauses: []string{"ship already sent"},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: a user middleware swallows the unknown outcome of a pivot with one attempt ; " +
				"Should: report that Run did not return sagas.ErrIncomplete",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return swallowing(shopSetup(t, sound, pivotFlow(1, false), noLate))
			},
			wantErrors: []string{`step "pivot", case "unknown outcome": Run returned <nil>, want sagas.ErrIncomplete`},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: the pivot never starts in its case and a later step does not start either ; " +
				"Should: report both steps, so a later case still runs",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				builds := 0

				return shopSetup(t, sound, func(book *ledger) *sagas.Group {
					builds++

					var err error
					if builds > 1 {
						err = errBoom
					}

					return sagas.Sequence(newShip(err), newLedgerPivot(book, 3, false), newNotify())
				}, noLate)
			},
			wantErrors: []string{
				`step "pivot", case "unknown outcome": the step did not start`,
				`step "notify", case "step repeated": the step did not start`,
			},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: both the pivot case and a forward-only case break their contract ; " +
				"Should: report the pivot case and then the forward-only case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, bothTwice, pivotFlow(3, false), noLate)
			},
			wantErrors: []string{
				`step "pivot", case "unknown outcome": Check: pivot applied more than once`,
				`step "ship", case "step repeated": Check: shipped more than once`,
			},
			wantSetups: 3,
		},
		{
			name: "[Fail] When: the flow has no pivot ; Should: report it and run no case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, chargeThenShip, noLate)
			},
			wantErrors: []string{"found no pivot"},
			wantSetups: 1,
		},
		{
			name: "[Fail] When: Setup drops mw ; Should: report that the clean run recorded no call and run no case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return droppingMiddleware(shopSetup(t, sound, pivotFlow(3, false), noLate))
			},
			wantErrors: []string{noCallReport},
			wantSetups: 1,
		},
		{
			name: "[Fail] When: the clean run fails ; Should: report it and run no case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, sound, func(book *ledger) *sagas.Group {
					return sagas.Sequence(newCharge(book), newLedgerPivot(book, 3, false), newShip(errBoom))
				}, noLate)
			},
			wantErrors: []string{"the clean run failed"},
			wantSetups: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			setup := tt.setup(t)
			setups := 0
			counted := func(mw ...sagas.Middleware) Case[probeState] {
				setups++

				return setup(mw...)
			}

			fake := &fakeTB{TB: t}
			Pivot(fake, counted)

			require.Len(t, fake.errors, len(tt.wantErrors), fake.errors)

			for i, want := range tt.wantErrors {
				assert.Contains(t, fake.errors[i], want)
			}

			require.LessOrEqual(t, len(tt.wantCauses), len(fake.errors))

			for i, cause := range tt.wantCauses {
				require.NotEmpty(t, cause)
				assert.Contains(t, fake.errors[i], cause)
			}

			assert.Equal(t, tt.wantSetups, setups)
		})
	}
}
