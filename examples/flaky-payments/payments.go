package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rilder-almeida/sagas"
)

const (
	exitFailure      = 1
	exitWriteFailure = 2

	cardFlaky   = "visa"
	cardRefused = "refused"
	cardSilent  = "silent"

	flakyCalls      = 2
	attempts        = 3
	attemptTimeout  = 50 * time.Millisecond
	retryDelay      = time.Millisecond
	slotInterval    = 2 * time.Millisecond
	breakerLimit    = 4
	breakerCooldown = time.Minute
)

var (
	errBusy     = errors.New("provider busy")
	errDeclined = errors.New("card declined")
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
	ID       string
	Card     string
	ChargeID string
}

func (o *order) ExecutionID() string { return o.ID }

// --8<-- [end:state]

type usage struct {
	calls   int
	refunds int
}

type provider struct {
	mu      sync.Mutex
	seen    map[string]int
	used    usage
	charges *store
}

func newProvider() *provider {
	return &provider{seen: map[string]int{}, charges: newStore("PAY")}
}

func (p *provider) charge(ctx context.Context, key, card string) (string, error) {
	p.mu.Lock()
	p.used.calls++
	p.seen[card]++
	seen := p.seen[card]
	p.mu.Unlock()

	switch {
	case card == cardRefused:
		return "", errDeclined
	case card == cardSilent:
		<-ctx.Done()

		return "", ctx.Err()
	case seen <= flakyCalls:
		return "", errBusy
	}

	return p.charges.put(ctx, key)
}

func (p *provider) refund(ctx context.Context, key string) error {
	p.mu.Lock()
	p.used.refunds++
	p.mu.Unlock()

	return p.charges.removeIfPresent(ctx, key)
}

func (p *provider) take() usage {
	p.mu.Lock()
	defer p.mu.Unlock()

	used := p.used
	p.used = usage{}

	return used
}

// --8<-- [start:steps]

type pay struct {
	sagas.Step

	provider *provider
	breaker  *breaker
}

func (p *pay) Execute(ctx context.Context, o *order) error {
	id, err := p.provider.charge(ctx, sagas.IdempotencyKey(ctx), o.Card)
	if errors.Is(err, errDeclined) {
		return sagas.Permanent(err)
	}

	if err != nil {
		return err
	}

	o.ChargeID = id

	return nil
}

func (p *pay) Compensate(ctx context.Context, _ *order) error {
	return p.provider.refund(ctx, sagas.IdempotencyKey(ctx))
}

// --8<-- [end:steps]

// --8<-- [start:ratelimit]

type rateLimit struct {
	mu    sync.Mutex
	every time.Duration
	next  time.Time
}

func (r *rateLimit) Wrap(ctx context.Context, _ sagas.CallInfo, call sagas.Call) error {
	r.mu.Lock()

	start := time.Now()
	if start.Before(r.next) {
		start = r.next
	}

	r.next = start.Add(r.every)
	r.mu.Unlock()

	timer := time.NewTimer(time.Until(start))
	defer timer.Stop()

	select {
	case <-timer.C:
		return call(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --8<-- [end:ratelimit]

// --8<-- [start:breaker]

var errOpen = errors.New("circuit open")

type breaker struct {
	mu       sync.Mutex
	limit    int
	cooldown time.Duration
	failures int
	openTill time.Time
}

func (b *breaker) Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
	if info.Phase == sagas.PhaseCompensate {
		return call(ctx)
	}

	b.mu.Lock()
	open := time.Now().Before(b.openTill)
	b.mu.Unlock()

	if open {
		return errOpen
	}

	err := call(ctx)

	b.mu.Lock()
	defer b.mu.Unlock()

	if err == nil {
		b.failures = 0

		return nil
	}

	b.failures++
	if b.failures >= b.limit {
		b.failures = 0
		b.openTill = time.Now().Add(b.cooldown)
	}

	return err
}

func (p *pay) Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
	return p.breaker.Wrap(ctx, info, call)
}

// --8<-- [end:breaker]

type services struct {
	provider *provider
	breaker  *breaker
	limit    *rateLimit
}

func newServices() *services {
	return &services{
		provider: newProvider(),
		breaker:  &breaker{limit: breakerLimit, cooldown: breakerCooldown},
		limit:    &rateLimit{every: slotInterval},
	}
}

// --8<-- [start:retry]

func newPay(s *services) *pay {
	return &pay{
		Retry:          sagas.RetryPolicy{Attempts: attempts, Initial: retryDelay, NoJitter: true},
		AttemptTimeout: attemptTimeout,
		provider:       s.provider,
		breaker:        s.breaker,
	}
}

// --8<-- [end:retry]

// --8<-- [start:saga]

func newSaga(s *services) (*sagas.Saga[order], error) {
	return sagas.New[order]("payment", sagas.Sequence(newPay(s)), sagas.WithMiddleware(s.limit))
}

// --8<-- [end:saga]

// --8<-- [start:run]

func run(w io.Writer) error {
	svc := newServices()

	saga, err := newSaga(svc)
	if err != nil {
		return err
	}

	orders := []order{
		{ID: "pay-1", Card: cardFlaky},
		{ID: "pay-2", Card: cardRefused},
		{ID: "pay-3", Card: cardSilent},
		{ID: "pay-4", Card: cardFlaky},
	}

	for i := range orders {
		runErr := saga.Run(context.Background(), &orders[i])

		if reportErr := report(w, &orders[i], svc.provider.take(), runErr); reportErr != nil {
			return reportErr
		}
	}

	return nil
}

func report(w io.Writer, o *order, used usage, runErr error) error {
	var failed *sagas.RunError

	switch {
	case runErr == nil:
		_, err := fmt.Fprintf(w, "%s: completed, provider calls: %d, refunds: %d\n", o.ID, used.calls, used.refunds)

		return err
	case errors.As(runErr, &failed):
		_, err := fmt.Fprintf(w, "%s: %s, pending: %v, outcome: %s, provider calls: %d, refunds: %d\n",
			o.ID, failed.Kind.Name(), failed.Pending, failed.Steps[0].Kind.Name(), used.calls, used.refunds)

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
