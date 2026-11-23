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

var errNoCapacity = errors.New("no capacity for this plan")

type store struct {
	mu      sync.Mutex
	prefix  string
	refused []string
	byKey   map[string]string
	issued  int
}

func newStore(prefix string, refused ...string) *store {
	return &store{prefix: prefix, refused: refused, byKey: map[string]string{}}
}

func (s *store) put(ctx context.Context, key, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.byKey[key]; ok {
		return id, nil
	}

	if slices.Contains(s.refused, value) {
		return "", fmt.Errorf("%w: %s", errNoCapacity, value)
	}

	s.issued++
	id := fmt.Sprintf("%s-%d", s.prefix, s.issued)
	s.byKey[key] = id

	return id, nil
}

func (s *store) remove(ctx context.Context, key string) error {
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

type account struct {
	ID        string
	Email     string
	Plan      string
	AccountID string
	ServerID  string
	ChargeID  string
	MailID    string
}

func (a *account) ExecutionID() string { return a.ID }

// --8<-- [end:state]

// --8<-- [start:steps]

type register struct {
	sagas.Step

	accounts *store
}

func (r *register) Execute(ctx context.Context, a *account) error {
	id, err := r.accounts.put(ctx, sagas.IdempotencyKey(ctx), a.Email)
	if err != nil {
		return err
	}

	a.AccountID = id

	return nil
}

func (r *register) Compensate(ctx context.Context, _ *account) error {
	return r.accounts.remove(ctx, sagas.IdempotencyKey(ctx))
}

type provision struct {
	sagas.Step

	servers *store
}

func (p *provision) Execute(ctx context.Context, a *account) error {
	id, err := p.servers.put(ctx, sagas.IdempotencyKey(ctx), a.Plan)
	if errors.Is(err, errNoCapacity) {
		return sagas.Permanent(err)
	}

	if err != nil {
		return err
	}

	a.ServerID = id

	return nil
}

func (p *provision) Compensate(ctx context.Context, _ *account) error {
	return p.servers.remove(ctx, sagas.IdempotencyKey(ctx))
}

type charge struct {
	sagas.Step

	payments *store
}

func (c *charge) Pivot(ctx context.Context, a *account) error {
	id, err := c.payments.put(ctx, sagas.IdempotencyKey(ctx), a.Plan)
	if err != nil {
		return err
	}

	a.ChargeID = id

	return nil
}

type welcome struct {
	sagas.Step

	mails *store
}

func (w *welcome) Execute(ctx context.Context, a *account) error {
	id, err := w.mails.put(ctx, sagas.IdempotencyKey(ctx), a.Email)
	if err != nil {
		return err
	}

	a.MailID = id

	return nil
}

// --8<-- [end:steps]

// --8<-- [start:saga]

func newSaga(accounts *store) (*sagas.Saga[account], error) {
	return sagas.New[account]("signup", sagas.Sequence(
		&register{accounts: accounts},
		&provision{servers: newStore("SRV", "dedicated")},
		&charge{payments: newStore("PAY")},
		&welcome{mails: newStore("MAIL")},
	))
}

// --8<-- [end:saga]

// --8<-- [start:run]

func run(w io.Writer) error {
	saga, err := newSaga(newStore("ACC"))
	if err != nil {
		return err
	}

	accounts := []account{
		{ID: "acct-1", Email: "ana@example.com", Plan: "basic"},
		{ID: "acct-2", Email: "bia@example.com", Plan: "dedicated"},
	}

	for i := range accounts {
		if reportErr := report(w, accounts[i].ID, saga.Run(context.Background(), &accounts[i])); reportErr != nil {
			return reportErr
		}
	}

	return nil
}

func report(w io.Writer, id string, runErr error) error {
	var failed *sagas.RunError

	switch {
	case runErr == nil:
		_, err := fmt.Fprintf(w, "%s: completed\n", id)

		return err
	case errors.As(runErr, &failed):
		_, err := fmt.Fprintf(w, "%s: %s, pending: %v\n", id, failed.Kind.Name(), failed.Pending)

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
