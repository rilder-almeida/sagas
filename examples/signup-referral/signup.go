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

type account struct {
	ID         string `sagas:"id"`
	Email      string
	Plan       string
	Referral   string
	AccountID  string
	ReferralID string
	ServerID   string
	ChargeID   string
	MailID     string
}

// --8<-- [end:state]

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

// --8<-- [start:steps]

type register struct {
	sagas.Step

	accounts *store
	undone   *undone
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
	return r.undone.release(ctx, r.accounts, "register")
}

type provision struct {
	sagas.Step

	servers *store
	undone  *undone
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
	return p.undone.release(ctx, p.servers, "provision")
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

// --8<-- [start:referral]

type referral struct {
	sagas.Step

	referrals *store
	undone    *undone
}

func (*referral) Enabled(a *account) bool { return a.Referral != "" }

func (r *referral) Execute(ctx context.Context, a *account) error {
	id, err := r.referrals.put(ctx, sagas.IdempotencyKey(ctx), a.Referral)
	if err != nil {
		return err
	}

	a.ReferralID = id

	return nil
}

func (r *referral) Compensate(ctx context.Context, _ *account) error {
	return r.undone.release(ctx, r.referrals, "referral")
}

// --8<-- [end:referral]

// --8<-- [start:saga]

type services struct {
	accounts  *store
	referrals *store
	servers   *store
	payments  *store
	mails     *store
	undone    *undone
}

func newServices() *services {
	return &services{
		accounts:  newStore("ACC"),
		referrals: newStore("REF"),
		servers:   newStore("SRV", "dedicated"),
		payments:  newStore("PAY"),
		mails:     newStore("MAIL"),
		undone:    &undone{},
	}
}

func newSaga(s *services) (*sagas.Saga[account], error) {
	return sagas.New[account]("signup", sagas.Sequence(
		&register{accounts: s.accounts, undone: s.undone},
		&referral{referrals: s.referrals, undone: s.undone},
		&provision{servers: s.servers, undone: s.undone},
		&charge{payments: s.payments},
		&welcome{mails: s.mails},
	))
}

// --8<-- [end:saga]

// --8<-- [start:run]

func run(w io.Writer) error {
	svc := newServices()

	saga, err := newSaga(svc)
	if err != nil {
		return err
	}

	accounts := []account{
		{ID: "acct-1", Email: "ana@example.com", Plan: "basic", Referral: "FRIEND"},
		{ID: "acct-2", Email: "bia@example.com", Plan: "basic"},
		{ID: "acct-3", Email: "cai@example.com", Plan: "dedicated"},
	}

	for i := range accounts {
		runErr := saga.Run(context.Background(), &accounts[i])

		if reportErr := report(w, &accounts[i], svc.undone.take(), runErr); reportErr != nil {
			return reportErr
		}
	}

	return nil
}

func report(w io.Writer, a *account, ran []string, runErr error) error {
	var failed *sagas.RunError

	switch {
	case runErr == nil && a.ReferralID == "":
		_, err := fmt.Fprintf(w, "%s: completed, referral skipped\n", a.ID)

		return err
	case runErr == nil:
		_, err := fmt.Fprintf(w, "%s: completed, referral credited\n", a.ID)

		return err
	case errors.As(runErr, &failed):
		_, err := fmt.Fprintf(w, "%s: %s, pending: %v, undone: %v\n", a.ID, failed.Kind.Name(), failed.Pending, ran)

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
