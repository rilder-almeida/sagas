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

type (
	note          string
	risk          string
	reservationID string
	chargeID      string
	shipmentID    string
)

type order struct {
	ID            string `sagas:"id"`
	Item          string
	Amount        int
	Note          note
	Risk          risk
	ReservationID reservationID
	ChargeID      chargeID
	ShipmentID    shipmentID
}

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
	sagas.Step[order, reservationID]

	warehouse *store
	undone    *undone
}

func (r *reserve) Execute(ctx context.Context, _ order) (reservationID, error) {
	id, err := r.warehouse.put(ctx, sagas.IdempotencyKey(ctx))

	return reservationID(id), err
}

func (r *reserve) Compensate(ctx context.Context, _ order) error {
	return r.undone.release(ctx, r.warehouse, "reserve")
}

const fraudLimit = 1000

var errFlagged = errors.New("order flagged as fraud")

type fraudCheck struct{ sagas.Step[order, risk] }

func (*fraudCheck) Execute(_ context.Context, o order) (risk, error) {
	if o.Amount > fraudLimit {
		return "", sagas.Permanent(errFlagged)
	}

	return "low", nil
}

type annotation struct {
	sagas.Step[order, note]

	text note
}

func (a *annotation) Execute(ctx context.Context, _ order) (note, error) { return a.text, ctx.Err() }

// --8<-- [end:parallel]

// --8<-- [start:steps]

type charge struct {
	sagas.Pivot[order, chargeID]

	payments *store
}

func (c *charge) Execute(ctx context.Context, _ order) (chargeID, error) {
	id, err := c.payments.put(ctx, sagas.IdempotencyKey(ctx))

	return chargeID(id), err
}

type ship struct {
	sagas.Step[order, shipmentID]

	carrier *store
}

func (s *ship) Execute(ctx context.Context, _ order) (shipmentID, error) {
	id, err := s.carrier.put(ctx, sagas.IdempotencyKey(ctx))

	return shipmentID(id), err
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

func (s *services) build(notes []note, mw ...sagas.Middleware) (*sagas.Saga[order], error) {
	return newSaga(s, notes,
		sagas.WithJournal(s.gate), sagas.WithMiddleware(slices.Concat([]sagas.Middleware{s.gate}, mw)...))
}

// --8<-- [start:saga]

func newSaga(s *services, notes []note, opts ...sagas.Option) (*sagas.Saga[order], error) {
	annotations := make([]sagas.Flow, 0, len(notes))
	for i, text := range notes {
		annotations = append(annotations, &annotation{Name: fmt.Sprintf("note-%d", i+1), text: text})
	}

	checks := []sagas.Flow{&reserve{warehouse: s.warehouse, undone: s.undone}, &fraudCheck{}}

	return sagas.New[order]("order", sagas.Sequence(
		sagas.Parallel(slices.Concat(checks, annotations)...),
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
		order order
		notes []note
	}{
		{order: order{ID: "ord-1", Item: "lamp", Amount: smallAmount}},
		{order: order{ID: "ord-2", Item: "lamp", Amount: hugeAmount}},
		{order: order{ID: "ord-3", Item: "lamp", Amount: smallAmount}, notes: []note{"stock held", "screened"}},
	}

	for i := range orders {
		svc := newServices()

		saga, err := svc.build(orders[i].notes)
		if err != nil {
			if _, writeErr := fmt.Fprintf(w, "%s: refused: %v\n", orders[i].order.ID, err); writeErr != nil {
				return writeErr
			}

			continue
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
		_, err := fmt.Fprintf(w, "%s: %s, pending: %v, undone: %v\n", o.ID, failed.Kind.Name(), failed.Pending, ran)

		return err
	default:
		return runErr
	}
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
