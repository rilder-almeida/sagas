package sagastest_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rilder-almeida/sagas"
	"github.com/rilder-almeida/sagas/sagastest"
)

var (
	errBoom     = errors.New("boom")
	errBalanced = errors.New("balance off")
)

type shop struct{}

type charge struct {
	sagas.Step

	calls atomic.Int32
}

func (c *charge) Execute(context.Context, *shop) error {
	c.calls.Add(1)

	return nil
}

func ExampleFail() {
	step := &charge{}
	step.Retry = sagas.NoRetry()

	saga, err := sagas.New[shop]("shop", step, sagas.WithMiddleware(sagastest.Fail("charge", errBoom)))
	if err != nil {
		panic(err)
	}

	err = saga.Run(context.Background(), &shop{})

	_, writeErr := fmt.Printf("compensated: %t\nboom: %t\ncharge calls: %d\n",
		errors.Is(err, sagas.ErrCompensated), errors.Is(err, errBoom), step.calls.Load())
	if writeErr != nil {
		panic(writeErr)
	}

	// Output:
	// compensated: true
	// boom: true
	// charge calls: 0
}

type ledger struct {
	mu       sync.Mutex
	balance  int
	charged  map[string]bool
	refunded map[string]bool
}

func (l *ledger) charge(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.charged[key] {
		return
	}

	l.charged[key] = true
	l.balance += 100
}

func (l *ledger) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.refunded[key] {
		return
	}

	l.refunded[key] = true
	if l.charged[key] {
		l.balance -= 100
	}
}

func (l *ledger) check() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	want := 0

	for key, on := range l.charged {
		if on && !l.refunded[key] {
			want += 100
		}
	}

	if l.balance != want {
		return fmt.Errorf("%w: balance %d, want %d", errBalanced, l.balance, want)
	}

	return nil
}

type ledgerCharge struct {
	sagas.Step

	book *ledger
}

func (c *ledgerCharge) Execute(ctx context.Context, _ *shop) error {
	c.book.charge(sagas.IdempotencyKey(ctx))

	return nil
}

func (c *ledgerCharge) Compensate(ctx context.Context, _ *shop) error {
	c.book.refund(sagas.IdempotencyKey(ctx))

	return nil
}

type capture struct {
	testing.TB

	errors []string
}

func (*capture) Helper() {}

func (*capture) Context() context.Context { return context.Background() }

func (c *capture) Errorf(format string, args ...any) {
	c.errors = append(c.errors, fmt.Sprintf(format, args...))
}

func (c *capture) Error(args ...any) { c.errors = append(c.errors, fmt.Sprint(args...)) }

func (*capture) Logf(string, ...any) {}

func ExampleCompensations() {
	setup := func(mw ...sagas.Middleware) sagastest.Case[shop] {
		book := &ledger{charged: map[string]bool{}, refunded: map[string]bool{}}
		step := &ledgerCharge{book: book}
		step.Name, step.Retry = "charge", sagas.NoRetry()

		saga, err := sagas.New[shop]("shop", step, sagas.WithMiddleware(mw...))
		if err != nil {
			panic(err)
		}

		return sagastest.Case[shop]{
			Saga:  saga,
			State: &shop{},
			Check: func(*shop) error { return book.check() },
			Flow:  step,
			Late: func(_ context.Context, _, key string) error {
				book.charge(key)

				return nil
			},
		}
	}

	tb := &capture{}
	sagastest.Compensations(tb, setup)

	for _, msg := range tb.errors {
		if _, err := fmt.Println(msg); err != nil {
			panic(err)
		}
	}

	// Output:
	// sagastest: step "charge", case "step lands after its compensation": Check: balance off: balance 100, want 0
}

type journalAcceptsGaps struct{ *sagas.MemoryJournal }

func (j journalAcceptsGaps) Append(ctx context.Context, e *sagas.Event) error {
	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored, err := j.Events(ctx, e.Execution)
	if err != nil && !errors.Is(err, sagas.ErrNoEvents) {
		return err
	}

	if e.Seq > len(stored)+1 {
		return nil
	}

	return j.MemoryJournal.Append(ctx, e)
}

func ExampleJournal() {
	tb := &capture{}
	sagastest.Journal(tb, func() sagas.Journal { return journalAcceptsGaps{sagas.NewMemoryJournal()} })

	for _, msg := range tb.errors {
		if _, err := fmt.Println(msg); err != nil {
			panic(err)
		}
	}

	// Output:
	// sagastest: journal rule "gap": Append of seq 3 after seq 1 returned nil, want an error
	// sagastest: journal rule "gap": Append of seq 2 on an empty journal returned nil, want an error
}
