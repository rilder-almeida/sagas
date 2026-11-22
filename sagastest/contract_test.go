package sagastest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type defect string

const (
	sound                defect = ""
	refundFailsUnapplied defect = "refund fails unapplied"
	refundTwice          defect = "refund twice"
	noTombstone          defect = "no tombstone"
)

const (
	price      = 100
	stepCharge = "charge"
	stepShip   = "ship"
)

var (
	errNoCharge     = errors.New("refund of a charge that never landed")
	errRefused      = errors.New("late refused")
	errRefundedMore = errors.New("refunded more than once")
	errUnbalanced   = errors.New("balance off")
	errNoLateAction = errors.New("no late action")
)

type ledger struct {
	mu      sync.Mutex
	defect  defect
	balance int
	charged map[string]bool
	refunds map[string]int
}

func newLedger(d defect) *ledger {
	return &ledger{defect: d, charged: map[string]bool{}, refunds: map[string]int{}}
}

func (l *ledger) charge(key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.charged[key] || (l.refunds[key] > 0 && l.defect != noTombstone) {
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
	case l.refunds[key] > 0 && l.defect != refundTwice:
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
	s.Name, s.Retry = "pivot", sagas.NoRetry()

	return s
}

func (*pivotStep) Pivot(context.Context, *probeState) error { return nil }

type notifyStep struct {
	sagas.Step
}

func newNotify() *notifyStep {
	s := &notifyStep{}
	s.Name, s.Retry = "notify", sagas.NoRetry()

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
			wantSteps:   []string{stepCharge, "pivot", stepShip, "notify"},
			wantPivot:   "pivot",
			wantForward: []string{stepShip, "notify"},
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

func shopSetup(
	t *testing.T, d defect, flow func(*ledger) *sagas.Group, late func(*ledger) lateFunc,
) Setup[probeState] {
	t.Helper()

	return func(mw ...sagas.Middleware) Case[probeState] {
		book := newLedger(d)
		f := flow(book)

		saga, err := sagas.New[probeState]("shop", f, sagas.WithMiddleware(mw...))
		require.NoError(t, err)

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

type fakeTB struct {
	testing.TB

	errors []string
	logs   []string
}

func (*fakeTB) Helper() {}

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
			name: "[Fail] When: the refund fails on a charge that never landed ; Should: report each case of the step",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, refundFailsUnapplied, chargeThenShip, lateCharge)
			},
			wantErrors: []string{
				`step "charge", case "never applied"`,
				`step "charge", case "compensation runs twice"`,
				`step "charge", case "step lands after its compensation"`,
			},
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
			name: "[Fail] When: the charge ignores the refund tombstone ; Should: report the late case",
			setup: func(t *testing.T) Setup[probeState] {
				t.Helper()

				return shopSetup(t, noTombstone, chargeThenShip, lateCharge)
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
				assert.Contains(t, fake.errors[i], want)
			}

			require.Len(t, fake.logs, len(tt.wantLogs), fake.logs)

			for i, want := range tt.wantLogs {
				assert.Contains(t, fake.logs[i], want)
			}

			assert.Equal(t, tt.wantSetups, setups)
		})
	}
}
