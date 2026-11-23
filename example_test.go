package sagas_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/rilder-almeida/sagas"
)

const tripID = "trip-42"

var (
	errNoRooms = errors.New("no rooms")
	errGaveUp  = errors.New("gave up")
)

type trip struct {
	ID                           string
	Flight, Hotel, Charge, Email string
}

func (t *trip) ExecutionID() string { return t.ID }

type flight struct{ sagas.Step }

func (*flight) Execute(ctx context.Context, t *trip) error {
	t.Flight = sagas.IdempotencyKey(ctx)

	return nil
}

func (*flight) Compensate(_ context.Context, t *trip) error {
	t.Flight = ""

	return nil
}

type hotel struct {
	sagas.Step

	err error
}

func (h *hotel) Execute(_ context.Context, t *trip) error {
	if h.err != nil {
		return h.err
	}

	t.Hotel = "booked"

	return nil
}

func (*hotel) Compensate(_ context.Context, t *trip) error {
	t.Hotel = ""

	return nil
}

type charge struct{ sagas.Step }

func (*charge) Pivot(_ context.Context, t *trip) error {
	t.Charge = "charged"

	return nil
}

type email struct{ sagas.Step }

func (*email) Execute(_ context.Context, t *trip) error {
	t.Email = "sent"

	return nil
}

func newTrip(hotelErr error, opts ...sagas.Option) *sagas.Saga[trip] {
	flow := sagas.Sequence(&flight{}, &hotel{err: hotelErr}, &charge{}, &email{})

	saga, err := sagas.New[trip]("trip", flow, opts...)
	if err != nil {
		panic(err)
	}

	return saga
}

func printRun(w io.Writer, t *trip, runErr error) error {
	var failed *sagas.RunError

	switch {
	case runErr == nil:
		_, err := fmt.Fprintf(w, "%s: completed\n", t.ID)

		return err
	case errors.As(runErr, &failed):
		steps := make([]string, 0, len(failed.Steps))
		for _, s := range failed.Steps {
			steps = append(steps, s.Step)
		}

		_, err := fmt.Fprintf(w, "%s: %s, failed: %v, pending: %v\n", t.ID, failed.Kind.Name(), steps, failed.Pending)

		return err
	default:
		return runErr
	}
}

func Example() {
	saga, err := sagas.New[trip]("trip", sagas.Sequence(&flight{}, &hotel{}))
	if err != nil {
		panic(err)
	}

	if err = saga.Run(context.Background(), &trip{ID: tripID}); err != nil {
		panic(err)
	}

	if _, err = fmt.Println("completed"); err != nil {
		panic(err)
	}

	// Output:
	// completed
}

func ExampleNew() {
	_, err := sagas.New[trip]("trip", sagas.Sequence(&flight{Name: "book"}, &flight{Name: "book"}))
	if _, werr := fmt.Println(err); werr != nil {
		panic(werr)
	}

	saga, err := sagas.New[trip]("trip", sagas.Sequence(&flight{}, &hotel{}))
	if err != nil {
		panic(err)
	}

	if _, err = fmt.Println(saga != nil); err != nil {
		panic(err)
	}

	// Output:
	// sagas: duplicate step: step "book": declared more than once
	// true
}

func ExampleSaga_Mermaid() {
	if _, err := fmt.Print(newTrip(nil).Mermaid()); err != nil {
		panic(err)
	}

	// Output:
	// flowchart TD
	//     s0["flight"]
	//     s1["hotel"]
	//     s2{{"charge"}}
	//     s3["email"]
	//     s0 --> s1
	//     s1 --> s2
	//     s2 --> s3
	//     classDef forwardOnly stroke-dasharray: 5 5
	//     class s3 forwardOnly
}

func ExampleSaga_Run() {
	ctx := context.Background()

	clean := &trip{ID: tripID}
	if err := printRun(os.Stdout, clean, newTrip(nil).Run(ctx, clean)); err != nil {
		panic(err)
	}

	failing := &trip{ID: tripID}
	if err := printRun(os.Stdout, failing, newTrip(sagas.Permanent(errNoRooms)).Run(ctx, failing)); err != nil {
		panic(err)
	}

	// Output:
	// trip-42: completed
	// trip-42: compensated, failed: [hotel], pending: []
}

func ExampleIdempotencyKey() {
	t := &trip{ID: tripID}
	if err := newTrip(nil).Run(context.Background(), t); err != nil {
		panic(err)
	}

	if _, err := fmt.Println(t.Flight); err != nil {
		panic(err)
	}

	// Output:
	// trip-42/flight
}

func ExampleSlog() {
	drop := func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey || a.Key == "duration" {
			return slog.Attr{}
		}

		return a
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: drop}))

	t := &trip{ID: tripID}
	if err := newTrip(nil, sagas.WithMiddleware(sagas.Slog{Logger: logger})).Run(context.Background(), t); err != nil {
		panic(err)
	}

	// Output:
	// level=INFO msg="sagas: attempt" saga=trip execution=trip-42 step=flight phase=execute attempt=1
	// level=INFO msg="sagas: attempt" saga=trip execution=trip-42 step=hotel phase=execute attempt=1
	// level=INFO msg="sagas: attempt" saga=trip execution=trip-42 step=charge phase=pivot attempt=1
	// level=INFO msg="sagas: attempt" saga=trip execution=trip-42 step=email phase=execute attempt=1
}

type seat struct {
	sagas.Step

	arrive chan<- struct{}
	peer   <-chan struct{}
	value  string
}

func (s *seat) Execute(ctx context.Context, t *trip) error {
	close(s.arrive)

	select {
	case <-s.peer:
	case <-ctx.Done():
		return ctx.Err()
	}

	t.Hotel = s.value

	return nil
}

func ExampleParallel() {
	ctx := context.Background()

	saga, err := sagas.New[trip]("trip", sagas.Parallel(&flight{}, &hotel{}))
	if err != nil {
		panic(err)
	}

	clean := &trip{ID: tripID}
	if err = printRun(os.Stdout, clean, saga.Run(ctx, clean)); err != nil {
		panic(err)
	}

	windowArrived, aisleArrived := make(chan struct{}), make(chan struct{})
	window := &seat{Name: "window", Retry: sagas.NoRetry(), arrive: windowArrived, peer: aisleArrived, value: "window"}
	aisle := &seat{Name: "aisle", Retry: sagas.NoRetry(), arrive: aisleArrived, peer: windowArrived, value: "aisle"}

	clash, err := sagas.New[trip]("trip", sagas.Parallel(window, aisle))
	if err != nil {
		panic(err)
	}

	runErr := clash.Run(ctx, &trip{ID: tripID})

	conflict, compensated := errors.Is(runErr, sagas.ErrConflict), errors.Is(runErr, sagas.ErrCompensated)
	if _, err = fmt.Printf("conflict: %t\ncompensated: %t\n", conflict, compensated); err != nil {
		panic(err)
	}

	// Output:
	// trip-42: completed
	// conflict: true
	// compensated: true
}

type promo struct{ sagas.Step }

func (*promo) Enabled(*trip) bool { return false }

func (*promo) Execute(_ context.Context, t *trip) error {
	t.Email = "sent"

	return nil
}

func ExampleStep_enabled() {
	saga, err := sagas.New[trip]("trip", sagas.Sequence(&flight{}, &promo{}))
	if err != nil {
		panic(err)
	}

	t := &trip{ID: tripID}
	if err = printRun(os.Stdout, t, saga.Run(context.Background(), t)); err != nil {
		panic(err)
	}

	if _, err = fmt.Printf("promo ran: %t\n", t.Email != ""); err != nil {
		panic(err)
	}

	// Output:
	// trip-42: completed
	// promo ran: false
}

func ExampleWithJournal() {
	ctx := context.Background()
	journal := sagas.NewMemoryJournal()

	if err := newTrip(nil, sagas.WithJournal(journal)).Run(ctx, &trip{ID: tripID}); err != nil {
		panic(err)
	}

	events, err := journal.Events(ctx, tripID)
	if err != nil {
		panic(err)
	}

	for i := range events {
		e := &events[i]
		if _, err = fmt.Println(strings.TrimSpace(fmt.Sprintf("%d %s %s", e.Seq, e.Kind, e.Step))); err != nil {
			panic(err)
		}
	}

	// Output:
	// 1 saga-started
	// 2 step-started flight
	// 3 step-succeeded flight
	// 4 step-started hotel
	// 5 step-succeeded hotel
	// 6 step-started charge
	// 7 step-succeeded charge
	// 8 step-started email
	// 9 step-succeeded email
	// 10 saga-finished
}

type abandon struct {
	sagas.Step

	cancel context.CancelFunc
	stuck  bool
}

func (a *abandon) Execute(ctx context.Context, _ *trip) error {
	a.cancel()
	<-ctx.Done()

	return ctx.Err()
}

func (a *abandon) Compensate(ctx context.Context, _ *trip) error {
	if !a.stuck {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Minute):
		return sagas.Permanent(errGaveUp)
	}
}

func ExampleWithRecoveryTimeout() {
	for _, tc := range []struct {
		grace time.Duration
		stuck bool
	}{{time.Minute, false}, {10 * time.Millisecond, true}} {
		ctx, cancel := context.WithCancel(context.Background())

		saga, err := sagas.New[trip]("trip", sagas.Sequence(&flight{}, &abandon{cancel: cancel, stuck: tc.stuck}),
			sagas.WithRecoveryTimeout(tc.grace))
		if err != nil {
			panic(err)
		}

		t := &trip{ID: tripID}
		runErr := saga.Run(ctx, t)

		cancel()

		if err = printRun(os.Stdout, t, runErr); err != nil {
			panic(err)
		}

		if _, err = fmt.Printf("grace ended: %t\n", errors.Is(runErr, sagas.ErrGraceEnded)); err != nil {
			panic(err)
		}
	}

	// Output:
	// trip-42: compensated, failed: [abandon], pending: []
	// grace ended: false
	// trip-42: compensation_failed, failed: [abandon abandon], pending: [flight abandon]
	// grace ended: true
}

func ExampleStep_name() {
	flow := sagas.Sequence(&flight{Name: "book-flight"}, &hotel{})

	reports, err := sagas.Inspect[trip](flow)
	if err != nil {
		panic(err)
	}

	for i := range reports {
		if _, err = fmt.Println(reports[i].Name); err != nil {
			panic(err)
		}
	}

	// Output:
	// book-flight
	// hotel
}

func ExampleStep_description() {
	reports, err := sagas.Inspect[trip](&charge{Description: "charge the card"})
	if err != nil {
		panic(err)
	}

	if _, err = fmt.Printf("%s: %s\n", reports[0].Name, reports[0].Description); err != nil {
		panic(err)
	}

	// Output:
	// charge: charge the card
}

type slow struct{ sagas.Step }

func (*slow) Execute(ctx context.Context, _ *trip) error {
	<-ctx.Done()

	return ctx.Err()
}

func ExampleStep_attemptTimeout() {
	saga, err := sagas.New[trip]("trip",
		&slow{AttemptTimeout: 10 * time.Millisecond, Retry: sagas.NoRetry()})
	if err != nil {
		panic(err)
	}

	var failed *sagas.RunError
	if runErr := saga.Run(context.Background(), &trip{ID: tripID}); !errors.As(runErr, &failed) {
		panic(runErr)
	}

	if _, err = fmt.Printf("%s: %s\n", failed.Steps[0].Step, failed.Steps[0].Kind.Name()); err != nil {
		panic(err)
	}

	// Output:
	// slow: unknown
}

func ExampleInspect() {
	reports, err := sagas.Inspect[trip](sagas.Sequence(&flight{}, &hotel{}, &charge{}, &email{}))
	if err != nil {
		panic(err)
	}

	for i := range reports {
		if _, err = fmt.Printf("%s after %v\n", reports[i].Name, reports[i].After); err != nil {
			panic(err)
		}
	}

	// Output:
	// flight after []
	// hotel after [flight]
	// charge after [hotel]
	// email after [charge]
}

func ExampleChain() {
	say := func(format string, args ...any) error {
		_, err := fmt.Printf(format+"\n", args...)

		return err
	}
	around := func(name string) sagas.MiddlewareFunc {
		return func(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
			if err := say("%s before %s", name, info.Step); err != nil {
				return err
			}

			callErr := call(ctx)

			return errors.Join(callErr, say("%s after %s", name, info.Step))
		}
	}

	saga, err := sagas.New[trip]("trip", &flight{}, sagas.WithMiddleware(sagas.Chain(around("outer"), around("inner"))))
	if err != nil {
		panic(err)
	}

	if err = saga.Run(context.Background(), &trip{ID: tripID}); err != nil {
		panic(err)
	}

	// Output:
	// outer before flight
	// inner before flight
	// inner after flight
	// outer after flight
}
