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

type (
	flightRef string
	hotelRef  string
	chargeRef string
	emailRef  string
)

type trip struct {
	ID     string `sagas:"id"`
	Flight flightRef
	Hotel  hotelRef
	Charge chargeRef
	Email  emailRef
}

type flight struct{ sagas.Step[trip, flightRef] }

func (*flight) Execute(ctx context.Context, _ trip) (flightRef, error) {
	return flightRef(sagas.IdempotencyKey(ctx)), nil
}

func (*flight) Compensate(context.Context, trip) error { return nil }

type hotel struct {
	sagas.Step[trip, hotelRef]

	err error
}

func (h *hotel) Execute(context.Context, trip) (hotelRef, error) {
	if h.err != nil {
		return "", h.err
	}

	return "booked", nil
}

func (*hotel) Compensate(context.Context, trip) error { return nil }

type charge struct{ sagas.Pivot[trip, chargeRef] }

func (*charge) Execute(context.Context, trip) (chargeRef, error) { return "charged", nil }

type email struct{ sagas.Step[trip, emailRef] }

func (*email) Execute(context.Context, trip) (emailRef, error) { return "sent", nil }

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

	if _, err := fmt.Printf("flight after the rollback: %q\n", failing.Flight); err != nil {
		panic(err)
	}

	// Output:
	// trip-42: completed
	// trip-42: compensated, failed: [hotel], pending: []
	// flight after the rollback: ""
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
	// trip/trip-42/flight
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

type seat struct{ sagas.Step[trip, hotelRef] }

func (s *seat) Execute(context.Context, trip) (hotelRef, error) { return hotelRef(s.Name), nil }

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

	_, err = sagas.New[trip]("trip", sagas.Parallel(&seat{Name: "window"}, &seat{Name: "aisle"}))
	if _, werr := fmt.Println(err); werr != nil {
		panic(werr)
	}

	// Output:
	// trip-42: completed
	// sagas: invalid step: steps "window" and "aisle": both produce sagas_test.hotelRef and may run at the same time
}

type promo struct{ sagas.Step[trip, emailRef] }

func (*promo) Enabled(context.Context, trip) bool { return false }

func (*promo) Execute(context.Context, trip) (emailRef, error) { return "sent", nil }

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

		line := strings.Fields(fmt.Sprintf("%d %s %s", e.Seq, e.Kind, e.Step))
		for _, v := range e.Values {
			line = append(line, v.Field+"="+string(v.Value))
		}

		if _, err = fmt.Println(strings.Join(line, " ")); err != nil {
			panic(err)
		}
	}

	// Output:
	// 1 saga-started ID="trip-42" Flight="" Hotel="" Charge="" Email=""
	// 2 step-started flight
	// 3 step-succeeded flight Flight="trip/trip-42/flight"
	// 4 step-started hotel
	// 5 step-succeeded hotel Hotel="booked"
	// 6 step-started charge
	// 7 step-succeeded charge Charge="charged"
	// 8 step-started email
	// 9 step-succeeded email Email="sent"
	// 10 saga-finished
}

type abandon struct {
	sagas.Step[trip, sagas.None]

	cancel context.CancelFunc
	stuck  bool
}

func (a *abandon) Execute(ctx context.Context, _ trip) error {
	a.cancel()
	<-ctx.Done()

	return ctx.Err()
}

func (a *abandon) Compensate(ctx context.Context, _ trip) error {
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
	// trip-42: compensation-failed, failed: [abandon abandon], pending: [flight abandon]
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

type slow struct{ sagas.Step[trip, sagas.None] }

func (*slow) Execute(ctx context.Context, _ trip) error {
	<-ctx.Done()

	return ctx.Err()
}

func ExampleStep_attemptTimeout() {
	saga, err := sagas.New[trip]("trip",
		&slow{AttemptTimeout: 10 * time.Millisecond, RetryPolicy: sagas.NoRetry()})
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
