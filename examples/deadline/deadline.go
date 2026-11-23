package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/rilder-almeida/sagas"
)

const (
	exitFailure      = 1
	exitWriteFailure = 2

	callerTimeout = 100 * time.Millisecond
	longGrace     = 5 * time.Second
	shortGrace    = 100 * time.Millisecond
	giveUpAfter   = time.Minute

	faultSlowUndo    = "slow-undo"
	faultInvoiceUndo = "invoice-undo"
)

var (
	errRefused = errors.New("invoice service refused the undo")
	errGaveUp  = errors.New("label service gave up waiting")
)

// --8<-- [start:store]

type store struct {
	mu     sync.Mutex
	prefix string
	byKey  map[string]string
	issued int
}

func newStore(prefix string) *store {
	return &store{prefix: prefix, byKey: map[string]string{}}
}

func (s *store) put(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.byKey[key]; ok {
		return id, nil
	}

	s.issued++
	id := fmt.Sprintf("%s-%d", s.prefix, s.issued)
	s.byKey[key] = id

	return id, nil
}

func (s *store) removeIfPresent(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.byKey, key)

	return nil
}

// --8<-- [end:store]

// --8<-- [start:state]

type order struct {
	ID        string
	Fault     string
	StockID   string
	InvoiceID string
	PackID    string
}

func (o *order) ExecutionID() string { return o.ID }

// --8<-- [end:state]

type failureGate struct {
	*sagas.MemoryJournal

	once sync.Once
	open chan struct{}
}

func newFailureGate() *failureGate {
	return &failureGate{MemoryJournal: sagas.NewMemoryJournal(), open: make(chan struct{})}
}

func (g *failureGate) Append(ctx context.Context, e *sagas.Event) error {
	if err := g.MemoryJournal.Append(ctx, e); err != nil {
		return err
	}

	ended := e.Kind == sagas.EventCompensationFailed || e.Kind == sagas.EventCompensationSucceeded
	if ended && e.Step == "invoice" {
		g.once.Do(func() { close(g.open) })
	}

	return nil
}

func (g *failureGate) Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
	if info.Step == "tag" && info.Phase == sagas.PhaseCompensate {
		select {
		case <-g.open:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return call(ctx)
}

type undone struct {
	mu    sync.Mutex
	names []string
}

func (u *undone) add(name string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.names = append(u.names, name)
}

func (u *undone) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()

	names := slices.Sorted(slices.Values(u.names))
	u.names = nil

	return names
}

func (u *undone) release(ctx context.Context, s *store, name string) error {
	if err := s.removeIfPresent(ctx, sagas.IdempotencyKey(ctx)); err != nil {
		return err
	}

	u.add(name)

	return nil
}

// --8<-- [start:steps]

type reserve struct {
	sagas.Step

	stock  *store
	undone *undone
}

func (r *reserve) Execute(ctx context.Context, o *order) error {
	id, err := r.stock.put(ctx, sagas.IdempotencyKey(ctx))
	if err != nil {
		return err
	}

	o.StockID = id

	return nil
}

func (r *reserve) Compensate(ctx context.Context, _ *order) error {
	return r.undone.release(ctx, r.stock, "reserve")
}

type invoice struct {
	sagas.Step

	ledger *store
	undone *undone
}

func (i *invoice) Execute(ctx context.Context, o *order) error {
	id, err := i.ledger.put(ctx, sagas.IdempotencyKey(ctx))
	if err != nil {
		return err
	}

	o.InvoiceID = id

	return nil
}

func (i *invoice) Compensate(ctx context.Context, o *order) error {
	if o.Fault == faultInvoiceUndo {
		return sagas.Permanent(errRefused)
	}

	return i.undone.release(ctx, i.ledger, "invoice")
}

type pack struct {
	sagas.Step

	dock   *store
	undone *undone
}

func (p *pack) Execute(ctx context.Context, o *order) error {
	id, err := p.dock.put(ctx, sagas.IdempotencyKey(ctx))
	if err != nil {
		return err
	}

	o.PackID = id

	return nil
}

func (p *pack) Compensate(ctx context.Context, _ *order) error {
	return p.undone.release(ctx, p.dock, "pack")
}

type tag struct {
	sagas.Step

	labels *store
	undone *undone
}

func (t *tag) Execute(ctx context.Context, _ *order) error {
	if _, err := t.labels.put(ctx, sagas.IdempotencyKey(ctx)); err != nil {
		return err
	}

	<-ctx.Done()

	return ctx.Err()
}

func (t *tag) Compensate(ctx context.Context, o *order) error {
	if o.Fault == faultSlowUndo {
		giveUp := time.NewTimer(giveUpAfter)
		defer giveUp.Stop()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-giveUp.C:
			return sagas.Permanent(errGaveUp)
		}
	}

	return t.undone.release(ctx, t.labels, "tag")
}

// --8<-- [end:steps]

type services struct {
	stock  *store
	ledger *store
	dock   *store
	labels *store
	undone *undone
	gate   *failureGate
}

func newServices() *services {
	return &services{
		stock:  newStore("STK"),
		ledger: newStore("INV"),
		dock:   newStore("PCK"),
		labels: newStore("LBL"),
		undone: &undone{},
		gate:   newFailureGate(),
	}
}

func (s *services) build(grace time.Duration, policy sagas.CompensationFailurePolicy) (*sagas.Saga[order], error) {
	return newSaga(s, grace, policy, sagas.WithJournal(s.gate), sagas.WithMiddleware(s.gate))
}

// --8<-- [start:saga]

func newSaga(
	s *services, grace time.Duration, policy sagas.CompensationFailurePolicy, opts ...sagas.Option,
) (*sagas.Saga[order], error) {
	return sagas.New[order]("deadline", sagas.Sequence(
		&reserve{stock: s.stock, undone: s.undone},
		sagas.Parallel(
			&invoice{CompensationFailure: policy, ledger: s.ledger, undone: s.undone},
			sagas.Sequence(
				&pack{dock: s.dock, undone: s.undone},
				&tag{labels: s.labels, undone: s.undone},
			),
		),
	), slices.Concat(opts, []sagas.Option{sagas.WithRecoveryTimeout(grace)})...)
}

// --8<-- [end:saga]

type scenario struct {
	name   string
	grace  time.Duration
	policy sagas.CompensationFailurePolicy
	fault  string
}

// --8<-- [start:grace]

func graceRuns() []scenario {
	return []scenario{
		{name: "grace-long", grace: longGrace},
		{name: "grace-short", grace: shortGrace, fault: faultSlowUndo},
	}
}

// --8<-- [end:grace]

// --8<-- [start:policies]

func policyRuns() []scenario {
	return []scenario{
		{name: "block", grace: longGrace, policy: sagas.CompensationBlock, fault: faultInvoiceUndo},
		{name: "continue", grace: longGrace, policy: sagas.CompensationContinue, fault: faultInvoiceUndo},
		{name: "halt", grace: longGrace, policy: sagas.CompensationHalt, fault: faultInvoiceUndo},
	}
}

// --8<-- [end:policies]

// --8<-- [start:run]

func run(w io.Writer) error {
	for _, sc := range slices.Concat(graceRuns(), policyRuns()) {
		svc := newServices()

		saga, err := svc.build(sc.grace, sc.policy)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), callerTimeout)
		o := order{ID: sc.name, Fault: sc.fault}
		runErr := saga.Run(ctx, &o)

		cancel()

		if reportErr := report(w, &o, svc.undone.take(), runErr); reportErr != nil {
			return reportErr
		}
	}

	return nil
}

func report(w io.Writer, o *order, ran []string, runErr error) error {
	var failed *sagas.RunError

	if !errors.As(runErr, &failed) {
		return fmt.Errorf("run %s: want a *sagas.RunError: %w", o.ID, runErr)
	}

	_, err := fmt.Fprintf(w, "%s: %s, pending: %v, undone: %v%s\n",
		o.ID, failed.Kind.Name(), failed.Pending, ran, graceNote(runErr))

	return err
}

func graceNote(runErr error) string {
	if errors.Is(runErr, sagas.ErrGraceEnded) {
		return ", grace ended"
	}

	return ""
}

// --8<-- [end:run]

func execute(stdout, stderr io.Writer) int {
	err := run(stdout)
	if err == nil {
		return 0
	}

	if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
		return exitWriteFailure
	}

	return exitFailure
}
