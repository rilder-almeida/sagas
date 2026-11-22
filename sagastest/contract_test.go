package sagastest

import (
	"context"
	"sync"
	"testing"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	price      = 100
	stepCharge = "charge"
	stepShip   = "ship"
)

type ledger struct {
	mu      sync.Mutex
	balance int
	charged map[string]bool
	refunds map[string]int
}

func newLedger() *ledger {
	return &ledger{charged: map[string]bool{}, refunds: map[string]int{}}
}

func (l *ledger) charge(key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.charged[key] || l.refunds[key] > 0 {
		return nil
	}

	l.charged[key] = true
	l.balance += price

	return nil
}

func (l *ledger) refund(key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.refunds[key] > 0 {
		return nil
	}

	l.refunds[key]++
	if l.charged[key] {
		l.balance -= price
	}

	return nil
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
				newCharge(newLedger()), newPivotStep(), sagas.Parallel(newShip(nil), newNotify()),
			),
			wantSteps:   []string{stepCharge, "pivot", stepShip, "notify"},
			wantPivot:   "pivot",
			wantForward: []string{stepShip, "notify"},
		},
		{
			name:      "[Success] When: the flow has no pivot ; Should: give no pivot name and an empty zone",
			flow:      sagas.Sequence(newCharge(newLedger()), newShip(nil)),
			wantSteps: []string{stepCharge, stepShip},
		},
		{
			name:      "[Success] When: a step has Enabled ; Should: still list it among the steps",
			flow:      sagas.Sequence(newDisabledCharge(newLedger()), newShip(nil)),
			wantSteps: []string{stepCharge, stepShip},
		},
		{
			name:    "[Fail] When: a step after the pivot has Compensate ; Should: return the error of Inspect",
			flow:    sagas.Sequence(newPivotStep(), newCharge(newLedger())),
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
