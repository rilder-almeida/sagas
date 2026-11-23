package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/rilder-almeida/sagas"
)

const (
	exitFailure      = 1
	exitWriteFailure = 2
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
	ID            string
	Item          string
	Amount        int
	Note          string
	Risk          string
	ReservationID string
	ChargeID      string
	ShipmentID    string
}

func (o *order) ExecutionID() string { return o.ID }

// --8<-- [end:state]

type commitGate struct {
	*sagas.MemoryJournal

	once sync.Once
	open chan struct{}
}

func newCommitGate() *commitGate {
	return &commitGate{MemoryJournal: sagas.NewMemoryJournal(), open: make(chan struct{})}
}

func (g *commitGate) Append(ctx context.Context, e *sagas.Event) error {
	if err := g.MemoryJournal.Append(ctx, e); err != nil {
		return err
	}

	if e.Kind == sagas.EventStepSucceeded && e.Step == "reserve" {
		g.once.Do(func() { close(g.open) })
	}

	return nil
}

func (g *commitGate) Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
	if info.Step == "fraud-check" && info.Phase == sagas.PhaseExecute {
		select {
		case <-g.open:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return call(ctx)
}

// --8<-- [start:undone]

type undone struct {
	mu    sync.Mutex
	names []string
}

func (u *undone) add(name string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.names = append(u.names, name)
}

func (u *undone) release(ctx context.Context, s *store, name string) error {
	if err := s.removeIfPresent(ctx, sagas.IdempotencyKey(ctx)); err != nil {
		return err
	}

	u.add(name)

	return nil
}

func (u *undone) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()

	names := u.names
	u.names = nil

	return names
}

// --8<-- [end:undone]

// --8<-- [start:parallel]

type reserve struct {
	sagas.Step

	warehouse *store
	undone    *undone
}

func (r *reserve) Execute(ctx context.Context, o *order) error {
	id, err := r.warehouse.put(ctx, sagas.IdempotencyKey(ctx))
	if err != nil {
		return err
	}

	o.ReservationID = id
	o.Note = "stock held"

	return nil
}

func (r *reserve) Compensate(ctx context.Context, _ *order) error {
	return r.undone.release(ctx, r.warehouse, "reserve")
}

const fraudLimit = 1000

var errFlagged = errors.New("order flagged as fraud")

type fraudCheck struct {
	sagas.Step

	annotate bool
}

func (f *fraudCheck) Execute(_ context.Context, o *order) error {
	if o.Amount > fraudLimit {
		return sagas.Permanent(errFlagged)
	}

	o.Risk = "low"

	if f.annotate {
		o.Note = "screened"
	}

	return nil
}

// --8<-- [end:parallel]

// --8<-- [start:steps]

type charge struct {
	sagas.Step

	payments *store
}

func (c *charge) Pivot(ctx context.Context, o *order) error {
	id, err := c.payments.put(ctx, sagas.IdempotencyKey(ctx))
	if err != nil {
		return err
	}

	o.ChargeID = id

	return nil
}

type ship struct {
	sagas.Step

	carrier *store
}

func (s *ship) Execute(ctx context.Context, o *order) error {
	id, err := s.carrier.put(ctx, sagas.IdempotencyKey(ctx))
	if err != nil {
		return err
	}

	o.ShipmentID = id

	return nil
}

// --8<-- [end:steps]

type services struct {
	warehouse *store
	payments  *store
	carrier   *store
	undone    *undone
	gate      *commitGate
}

func newServices() *services {
	return &services{
		warehouse: newStore("SKU"),
		payments:  newStore("PAY"),
		carrier:   newStore("SHP"),
		undone:    &undone{},
		gate:      newCommitGate(),
	}
}

func (s *services) build(annotate bool, mw ...sagas.Middleware) (*sagas.Saga[order], error) {
	return newSaga(s, annotate,
		sagas.WithJournal(s.gate), sagas.WithMiddleware(slices.Concat([]sagas.Middleware{s.gate}, mw)...))
}

// --8<-- [start:saga]

func newSaga(s *services, annotate bool, opts ...sagas.Option) (*sagas.Saga[order], error) {
	return sagas.New[order]("order", sagas.Sequence(
		sagas.Parallel(
			&reserve{warehouse: s.warehouse, undone: s.undone},
			&fraudCheck{annotate: annotate},
		),
		&charge{payments: s.payments},
		&ship{carrier: s.carrier},
	), opts...)
}

// --8<-- [end:saga]

// --8<-- [start:run]

const (
	smallAmount = 80
	hugeAmount  = 5000
)

func run(w io.Writer) error {
	orders := []struct {
		order    order
		annotate bool
	}{
		{order: order{ID: "ord-1", Item: "lamp", Amount: smallAmount}},
		{order: order{ID: "ord-2", Item: "lamp", Amount: hugeAmount}},
		{order: order{ID: "ord-3", Item: "lamp", Amount: smallAmount}, annotate: true},
	}

	for i := range orders {
		svc := newServices()

		saga, err := svc.build(orders[i].annotate)
		if err != nil {
			return err
		}

		runErr := saga.Run(context.Background(), &orders[i].order)

		if reportErr := report(w, &orders[i].order, svc.undone.take(), runErr); reportErr != nil {
			return reportErr
		}
	}

	return nil
}

func report(w io.Writer, o *order, ran []string, runErr error) error {
	var failed *sagas.RunError

	switch {
	case runErr == nil:
		_, err := fmt.Fprintf(w, "%s: completed\n", o.ID)

		return err
	case errors.As(runErr, &failed):
		_, err := fmt.Fprintf(w, "%s: %s, pending: %v, undone: %v%s\n",
			o.ID, failed.Kind.Name(), failed.Pending, ran, conflictNote(failed))

		return err
	default:
		return runErr
	}
}

func conflictNote(failed *sagas.RunError) string {
	for _, step := range failed.Steps {
		if errors.Is(step, sagas.ErrConflict) {
			return ", conflict in: " + step.Step
		}
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
