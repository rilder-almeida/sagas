package sagastest

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
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
	sagas.Step[probeState, sagas.None]

	book *ledger
}

func newCharge(book *ledger) *chargeStep {
	s := &chargeStep{book: book}
	s.Name, s.RetryPolicy = stepCharge, sagas.NoRetry()
	s.RecoveryPolicy = sagas.RecoveryPolicy{Attempts: 1}

	return s
}

func (s *chargeStep) Execute(ctx context.Context, _ probeState) error {
	return s.book.charge(sagas.IdempotencyKey(ctx))
}

func (s *chargeStep) Compensate(ctx context.Context, _ probeState) error {
	return s.book.refund(sagas.IdempotencyKey(ctx))
}

type disabledCharge struct {
	chargeStep
}

func newDisabledCharge(book *ledger) *disabledCharge {
	s := &disabledCharge{book: book}
	s.Name, s.RetryPolicy = stepCharge, sagas.NoRetry()
	s.RecoveryPolicy = sagas.RecoveryPolicy{Attempts: 1}

	return s
}

func (*disabledCharge) Enabled(context.Context, probeState) bool { return false }

type shipStep struct {
	sagas.Step[probeState, sagas.None]

	err error
}

func newShip(err error) *shipStep {
	s := &shipStep{err: err}
	s.Name, s.RetryPolicy = stepShip, sagas.NoRetry()

	return s
}

func newForwardShip(err error) *shipStep {
	s := newShip(err)
	forwardOnly(&s.Step)

	return s
}

func forwardOnly(s *sagas.Step[probeState, sagas.None]) {
	s.RetryPolicy, s.RecoveryPolicy = sagas.RetryPolicy{}, sagas.RecoveryPolicy{Attempts: 1}
}

func (s *shipStep) Execute(context.Context, probeState) error { return s.err }

type pivotStep struct {
	sagas.Pivot[probeState, sagas.None]
}

func newPivotStep() *pivotStep {
	s := &pivotStep{}
	s.Name, s.RetryPolicy = stepPivot, sagas.NoRetry()

	return s
}

func (*pivotStep) Execute(context.Context, probeState) error { return nil }

type ledgerPivot struct {
	sagas.Pivot[probeState, count]

	book  *ledger
	bump  bool
	calls atomic.Int32
}

func newLedgerPivot(book *ledger, attempts int, bump bool) *ledgerPivot {
	s := &ledgerPivot{book: book, bump: bump}
	s.Name = stepPivot
	s.RetryPolicy = sagas.RetryPolicy{Attempts: attempts, Initial: time.Millisecond, NoJitter: true}

	return s
}

func (s *ledgerPivot) Execute(ctx context.Context, _ probeState) (count, error) {
	var n count
	if c := count(s.calls.Add(1)); s.bump {
		n = c
	}

	return n, s.book.pivot(sagas.IdempotencyKey(ctx))
}

type ledgerShip struct {
	sagas.Step[probeState, sagas.None]

	book *ledger
}

func newLedgerShip(book *ledger) *ledgerShip {
	s := &ledgerShip{book: book}
	s.Name, s.RecoveryPolicy = stepShip, sagas.RecoveryPolicy{Attempts: 1}

	return s
}

func (s *ledgerShip) Execute(ctx context.Context, _ probeState) error {
	return s.book.ship(sagas.IdempotencyKey(ctx))
}

type disabledShip struct {
	ledgerShip
}

func newDisabledShip(book *ledger) *disabledShip {
	s := &disabledShip{}
	s.book = book
	s.Name, s.RetryPolicy = stepShip, sagas.NoRetry()

	return s
}

func newForwardDisabledShip(book *ledger) *disabledShip {
	s := newDisabledShip(book)
	forwardOnly(&s.Step)

	return s
}

func (*disabledShip) Enabled(context.Context, probeState) bool { return false }

type notifyStep struct {
	sagas.Step[probeState, sagas.None]
}

func newNotify() *notifyStep {
	s := &notifyStep{}
	s.Name, s.RecoveryPolicy = stepNotify, sagas.RecoveryPolicy{Attempts: 1}

	return s
}

func (*notifyStep) Execute(context.Context, probeState) error { return nil }

type shopServices struct {
	book      *ledger
	nth       int
	flowCalls int
	build     func(s *shopServices) *sagas.Group
	late      lateAction
	opts      []sagas.Option
}

type lateAction func(book *ledger, step, key string) error

func newShopServices(d defect, nth int, build func(*shopServices) *sagas.Group, late lateAction) *shopServices {
	return &shopServices{book: newLedger(d), nth: nth, build: build, late: late}
}

func (s *shopServices) Flow() *sagas.Group {
	s.flowCalls++

	return s.build(s)
}

func (s *shopServices) Check(context.Context, *probeState) error { return s.book.check() }

func (s *shopServices) ExecuteLate(_ context.Context, _ *probeState, step, key string) error {
	return s.late(s.book, step, key)
}

func (s *shopServices) Options() []sagas.Option { return s.opts }

func lateCharge(book *ledger, step, key string) error {
	if step != stepCharge {
		return fmt.Errorf("%w: step %q", errNoLateAction, step)
	}

	return book.charge(key)
}

func lateRefused(*ledger, string, string) error { return errRefused }

func lateChargeRefused(book *ledger, step, key string) error {
	return errors.Join(lateCharge(book, step, key), errRefused)
}

func chargeThenShip(s *shopServices) *sagas.Group {
	return sagas.Sequence(newCharge(s.book), newShip(nil))
}

func shipFailsInCases(next func(book *ledger) sagas.Flow) func(*shopServices) *sagas.Group {
	return func(s *shopServices) *sagas.Group {
		var err error
		if s.nth > 1 {
			err = errBoom
		}

		return sagas.Sequence(newShip(err), next(s.book))
	}
}

func pivotFlow(attempts int, bump bool) func(*shopServices) *sagas.Group {
	return func(s *shopServices) *sagas.Group {
		return sagas.Sequence(newCharge(s.book), newLedgerPivot(s.book, attempts, bump), newLedgerShip(s.book))
	}
}

func swallowing() []sagas.Option {
	return []sagas.Option{sagas.WithMiddleware(sagas.MiddlewareFunc(
		func(ctx context.Context, _ sagas.CallInfo, call sagas.Call) error {
			if err := call(ctx); !errors.Is(err, context.DeadlineExceeded) {
				return err
			}

			return nil
		},
	))}
}

type caseResult struct {
	name string
	err  error
}

func runAll[W any](ctx context.Context, cases iter.Seq[Case[W]], newServices func() W) []caseResult {
	var results []caseResult

	for c := range cases {
		results = append(results, caseResult{name: c.Name, err: c.Run(ctx, newServices())})
	}

	return results
}

func assertResults(t *testing.T, results []caseResult, wantCases []string, wantErrs [][]error) {
	t.Helper()

	names := make([]string, 0, len(results))
	for _, r := range results {
		names = append(names, r.name)
	}

	require.Equal(t, wantCases, names)
	require.Len(t, wantErrs, len(results))

	for i, r := range results {
		if len(wantErrs[i]) == 0 {
			require.NoError(t, r.err, r.name)
		}

		for _, want := range wantErrs[i] {
			assert.ErrorIs(t, r.err, want, r.name)
		}
	}
}

func assertProbe(t *testing.T, err error, wantErr []error) {
	t.Helper()

	if len(wantErr) == 0 {
		require.NoError(t, err)
	}

	for _, want := range wantErr {
		require.ErrorIs(t, err, want)
	}
}

func compensationNames(steps ...string) []string {
	names := make([]string, 0, 3*len(steps))
	for _, step := range steps {
		names = append(names, step+" never applied", step+" compensation runs twice",
			step+" step lands after its compensation")
	}

	return names
}

func pivotNames(forwardOnly ...string) []string {
	names := make([]string, 0, 1+len(forwardOnly))
	names = append(names, stepPivot+" unknown outcome")

	for _, step := range forwardOnly {
		names = append(names, step+" step repeated")
	}

	return names
}

func Test_CompensationCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		defect    defect
		flow      func(*shopServices) *sagas.Group
		late      lateAction
		wantErr   []error
		wantCases []string
		wantErrs  [][]error
	}{
		{
			name:      "[Success] When: the ledger keeps tombstones and refunds once ; Should: pass every case",
			flow:      chargeThenShip,
			late:      lateCharge,
			wantCases: compensationNames(stepCharge),
			wantErrs:  [][]error{nil, nil, nil},
		},
		{
			name: "[Fail] When: the refund fails on a charge that never landed ; " +
				"Should: fail the cases where the charge never landed with ErrOutcome",
			defect:    refundFailsUnapplied,
			flow:      chargeThenShip,
			late:      lateCharge,
			wantCases: compensationNames(stepCharge),
			wantErrs:  [][]error{{ErrOutcome, errNoCharge}, nil, {ErrOutcome, errNoCharge}},
		},
		{
			name: "[Fail] When: the refund posts twice and the run still ends compensated ; " +
				"Should: fail the twice case with ErrCheck",
			defect:    refundTwice,
			flow:      chargeThenShip,
			late:      lateCharge,
			wantCases: compensationNames(stepCharge),
			wantErrs:  [][]error{nil, {ErrCheck, errRefundedMore}, nil},
		},
		{
			name: "[Fail] When: the refund of a landed charge has no guard against a second refund ; " +
				"Should: fail the twice case with ErrCheck",
			defect:    refundLandedTwice,
			flow:      chargeThenShip,
			late:      lateCharge,
			wantCases: compensationNames(stepCharge),
			wantErrs:  [][]error{nil, {ErrCheck, errRefundedMore}, nil},
		},
		{
			name: "[Fail] When: the charge ignores the refund tombstone ; " +
				"Should: fail the late case with ErrCheck",
			defect:    chargeIgnoresRefund,
			flow:      chargeThenShip,
			late:      lateCharge,
			wantCases: compensationNames(stepCharge),
			wantErrs:  [][]error{nil, nil, {ErrCheck, errUnbalanced}},
		},
		{
			name:      "[Success] When: ExecuteLate returns an error and Check passes ; Should: drop the reply",
			flow:      chargeThenShip,
			late:      lateRefused,
			wantCases: compensationNames(stepCharge),
			wantErrs:  [][]error{nil, nil, nil},
		},
		{
			name: "[Fail] When: ExecuteLate returns an error and Check fails ; " +
				"Should: join the reply with the Check error",
			defect:    chargeIgnoresRefund,
			flow:      chargeThenShip,
			late:      lateChargeRefused,
			wantCases: compensationNames(stepCharge),
			wantErrs:  [][]error{nil, nil, {ErrCheck, errUnbalanced, errRefused}},
		},
		{
			name: "[Fail] When: Flow names fewer steps than the clean run called ; " +
				"Should: return ErrCleanRun and no case",
			flow: func(s *shopServices) *sagas.Group {
				if s.flowCalls == 1 {
					return sagas.Sequence(newCharge(s.book))
				}

				return chargeThenShip(s)
			},
			late:    lateCharge,
			wantErr: []error{ErrCleanRun},
		},
		{
			name: "[Fail] When: Flow is not a valid flow ; Should: return ErrCleanRun with the error of Inspect",
			flow: func(s *shopServices) *sagas.Group {
				return sagas.Sequence(newPivotStep(), newCharge(s.book))
			},
			late:    lateCharge,
			wantErr: []error{ErrCleanRun, sagas.ErrInvalidStep},
		},
		{
			name: "[Fail] When: a step before the compensable step fails only in the cases ; " +
				"Should: fail each case with ErrNotStarted and the error of Run",
			flow:      shipFailsInCases(func(book *ledger) sagas.Flow { return newCharge(book) }),
			late:      lateCharge,
			wantCases: compensationNames(stepCharge),
			wantErrs:  [][]error{{ErrNotStarted, errBoom}, {ErrNotStarted, errBoom}, {ErrNotStarted, errBoom}},
		},
		{
			name: "[Fail] When: the compensable step is disabled ; Should: fail each case with ErrNotStarted",
			flow: func(s *shopServices) *sagas.Group {
				return sagas.Sequence(newDisabledCharge(s.book), newShip(nil))
			},
			late:      lateCharge,
			wantCases: compensationNames(stepCharge),
			wantErrs:  [][]error{{ErrNotStarted}, {ErrNotStarted}, {ErrNotStarted}},
		},
		{
			name: "[Fail] When: the clean run fails ; Should: return ErrCleanRun with the error of Run",
			flow: func(s *shopServices) *sagas.Group {
				return sagas.Sequence(newCharge(s.book), newShip(errBoom))
			},
			late:    lateCharge,
			wantErr: []error{ErrCleanRun, errBoom},
		},
		{
			name:    "[Fail] When: no step has Compensate ; Should: return ErrNoStepToTest",
			flow:    func(*shopServices) *sagas.Group { return sagas.Sequence(newShip(nil)) },
			late:    lateCharge,
			wantErr: []error{ErrNoStepToTest},
		},
		{
			name: "[Fail] When: every step is disabled in the given state ; " +
				"Should: return ErrCleanRun because the clean run recorded no call",
			flow: func(s *shopServices) *sagas.Group {
				return sagas.Sequence(newDisabledCharge(s.book), newDisabledShip(s.book))
			},
			late:    lateCharge,
			wantErr: []error{ErrCleanRun},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			nth := 0
			newServices := func() *shopServices {
				nth++

				return newShopServices(tt.defect, nth, tt.flow, tt.late)
			}

			cases, err := CompensationCases(t.Context(), "shop", probeState{ID: runID}, newServices())
			assertProbe(t, err, tt.wantErr)

			if err != nil {
				assert.Nil(t, cases)

				return
			}

			assertResults(t, runAll(t.Context(), cases, newServices), tt.wantCases, tt.wantErrs)
		})
	}
}

func Test_PivotCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		defect    defect
		flow      func(*shopServices) *sagas.Group
		opts      []sagas.Option
		wantErr   []error
		wantCases []string
		wantErrs  [][]error
	}{
		{
			name: "[Success] When: the pivot has three attempts and the forward-only step is idempotent ; " +
				"Should: pass every case",
			flow:      pivotFlow(3, false),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{nil, nil},
		},
		{
			name: "[Success] When: the pivot has one attempt ; " +
				"Should: expect an incomplete run with only the pivot pending",
			flow:      pivotFlow(1, false),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{nil, nil},
		},
		{
			name: "[Success] When: the pivot has zero attempts ; " +
				"Should: read the default attempts and expect the completed path",
			flow:      pivotFlow(0, false),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{nil, nil},
		},
		{
			name: "[Fail] When: the forward-only step ships twice when repeated ; " +
				"Should: fail the repeat case with ErrCheck",
			defect:    shipsTwice,
			flow:      pivotFlow(3, false),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{nil, {ErrCheck, errShippedMore}},
		},
		{
			name: "[Fail] When: the pivot applies its effect again after the unknown outcome ; " +
				"Should: fail the pivot case with ErrCheck",
			defect:    pivotsTwice,
			flow:      pivotFlow(3, false),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{{ErrCheck, errPivotedMore}, nil},
		},
		{
			name: "[Fail] When: the final state differs from the state of the clean run ; " +
				"Should: fail the pivot case with ErrOutcome",
			flow:      pivotFlow(3, true),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{{ErrOutcome}, nil},
		},
		{
			name: "[Fail] When: a forward-only step is disabled ; Should: fail the repeat case with ErrNotStarted",
			flow: func(s *shopServices) *sagas.Group {
				return sagas.Sequence(
					newCharge(s.book), newLedgerPivot(s.book, 3, false), newForwardDisabledShip(s.book),
				)
			},
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{nil, {ErrNotStarted}},
		},
		{
			name: "[Fail] When: the pivot returns an error when it is called again with the same key ; " +
				"Should: fail the pivot case with ErrOutcome and the error of Run",
			defect:    pivotErrors,
			flow:      pivotFlow(3, false),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{{ErrOutcome, errPivotAgain}, nil},
		},
		{
			name: "[Fail] When: the forward-only step returns an error when it is called again ; " +
				"Should: fail the repeat case with ErrOutcome and the error of Run",
			defect:    shipErrors,
			flow:      pivotFlow(3, false),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{nil, {ErrOutcome, errShipAgain}},
		},
		{
			name: "[Fail] When: a middleware of Options swallows the unknown outcome of a pivot with one attempt ; " +
				"Should: fail the pivot case with ErrOutcome",
			flow:      pivotFlow(1, false),
			opts:      swallowing(),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{{ErrOutcome}, nil},
		},
		{
			name: "[Fail] When: the pivot never starts in its case and a later step does not start either ; " +
				"Should: fail both cases with ErrNotStarted",
			flow: shipFailsInCases(func(book *ledger) sagas.Flow {
				return sagas.Sequence(newLedgerPivot(book, 3, false), newNotify())
			}),
			wantCases: pivotNames(stepNotify),
			wantErrs:  [][]error{{ErrNotStarted, errBoom}, {ErrNotStarted, errBoom}},
		},
		{
			name: "[Fail] When: both the pivot case and a forward-only case break their contract ; " +
				"Should: fail the pivot case and then the forward-only case with ErrCheck",
			defect:    bothTwice,
			flow:      pivotFlow(3, false),
			wantCases: pivotNames(stepShip),
			wantErrs:  [][]error{{ErrCheck, errPivotedMore}, {ErrCheck, errShippedMore}},
		},
		{
			name:    "[Fail] When: the flow has no pivot ; Should: return ErrNoStepToTest",
			flow:    chargeThenShip,
			wantErr: []error{ErrNoStepToTest},
		},
		{
			name: "[Fail] When: the clean run fails ; Should: return ErrCleanRun with the error of Run",
			flow: func(s *shopServices) *sagas.Group {
				return sagas.Sequence(newCharge(s.book), newLedgerPivot(s.book, 3, false), newForwardShip(errBoom))
			},
			wantErr: []error{ErrCleanRun, errBoom},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			nth := 0
			newServices := func() *shopServices {
				nth++
				s := newShopServices(tt.defect, nth, tt.flow, nil)
				s.opts = tt.opts

				return s
			}

			cases, err := PivotCases(t.Context(), "shop", probeState{ID: runID}, newServices())
			assertProbe(t, err, tt.wantErr)

			if err != nil {
				assert.Nil(t, cases)

				return
			}

			assertResults(t, runAll(t.Context(), cases, newServices), tt.wantCases, tt.wantErrs)
		})
	}
}

type eventLog struct {
	mu   sync.Mutex
	seqs []int
}

func (l *eventLog) Observe(_ context.Context, e *sagas.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.seqs = append(l.seqs, e.Seq)
}

type queueClose struct {
	err  error
	seqs []int
}

type factoryLog struct {
	mu     sync.Mutex
	names  []string
	closes []queueClose
}

func (f *factoryLog) called(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.names = append(f.names, name)

	return len(f.names)
}

func (f *factoryLog) closed(c queueClose) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closes = append(f.closes, c)
}

func observed(t *testing.T, log *factoryLog, s *shopServices) {
	t.Helper()

	events := &eventLog{}
	queue := sagas.NewMemoryEventQueue()
	s.opts = []sagas.Option{sagas.WithObserver(events), sagas.WithEventQueue(queue)}

	t.Cleanup(func() {
		err := queue.Close(context.Background())

		events.mu.Lock()
		defer events.mu.Unlock()

		log.closed(queueClose{err: err, seqs: slices.Clone(events.seqs)})
	})
}

func plainServices(*testing.T, *factoryLog, *shopServices) {}

func subtestNames(parent string, cases []string) []string {
	names := make([]string, 0, 1+len(cases))
	names = append(names, parent)

	for _, c := range cases {
		names = append(names, parent+"/"+strings.ReplaceAll(c, " ", "_"))
	}

	return names
}

func assertQueues(t *testing.T, closes []queueClose, want int) {
	t.Helper()

	require.Len(t, closes, want)

	for _, c := range closes {
		require.NoError(t, c.err)
		require.NotEmpty(t, c.seqs)

		for i, seq := range c.seqs {
			assert.Equal(t, i+1, seq)
		}
	}
}

func Test_TestCompensations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		flow       func(*shopServices) *sagas.Group
		wire       func(*testing.T, *factoryLog, *shopServices)
		wantCases  []string
		wantQueues int
	}{
		{
			name: "[Success] When: the ledger is sound ; " +
				"Should: call the factory with the parent t and then once per case with the subtest t",
			flow:      chargeThenShip,
			wire:      plainServices,
			wantCases: compensationNames(stepCharge),
		},
		{
			name: "[Success] When: two steps have a compensation ; " +
				"Should: run three cases for each of them in order",
			flow: func(s *shopServices) *sagas.Group {
				refund := newCharge(s.book)
				refund.Name = "refund"

				return sagas.Sequence(newCharge(s.book), refund)
			},
			wire:      plainServices,
			wantCases: compensationNames(stepCharge, "refund"),
		},
		{
			name: "[Success] When: the factory makes an event queue per case and closes it in t.Cleanup ; " +
				"Should: close each case queue with nil after its observer got each event once",
			flow:       chargeThenShip,
			wire:       observed,
			wantCases:  compensationNames(stepCharge),
			wantQueues: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			log := &factoryLog{}

			TestCompensations(t, "shop", probeState{ID: runID}, func(t *testing.T) *shopServices {
				t.Helper()

				s := newShopServices(sound, log.called(t.Name()), tt.flow, lateCharge)
				tt.wire(t, log, s)

				return s
			})

			assert.Equal(t, subtestNames(t.Name(), tt.wantCases), log.names)
			assertQueues(t, log.closes, tt.wantQueues)
		})
	}
}

func Test_TestPivot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		flow      func(*shopServices) *sagas.Group
		wantCases []string
	}{
		{
			name: "[Success] When: the pivot has three attempts ; " +
				"Should: call the factory with the parent t and then once per case with the subtest t",
			flow:      pivotFlow(3, false),
			wantCases: pivotNames(stepShip),
		},
		{
			name: "[Success] When: two forward-only steps follow the pivot ; " +
				"Should: run one repeat case for each of them after the pivot case",
			flow: func(s *shopServices) *sagas.Group {
				return sagas.Sequence(newLedgerPivot(s.book, 1, false), newLedgerShip(s.book), newNotify())
			},
			wantCases: pivotNames(stepShip, stepNotify),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			log := &factoryLog{}

			TestPivot(t, "shop", probeState{ID: runID}, func(t *testing.T) *shopServices {
				t.Helper()

				return newShopServices(sound, log.called(t.Name()), tt.flow, nil)
			})

			assert.Equal(t, subtestNames(t.Name(), tt.wantCases), log.names)
		})
	}
}
