package sagas_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/rilder-almeida/sagas"
)

const tripID = "trip-42"

var errNoRooms = errors.New("no rooms")

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

func printRun(t *trip, err error) {
	var runErr *sagas.RunError

	switch {
	case err == nil:
		fmt.Printf("%s: completed\n", t.ID)
	case errors.As(err, &runErr):
		failed := make([]string, 0, len(runErr.Steps))
		for _, s := range runErr.Steps {
			failed = append(failed, s.Step)
		}

		fmt.Printf("%s: %s, failed: %v, pending: %v\n", t.ID, runErr.Kind.Name(), failed, runErr.Pending)
	default:
		panic(err)
	}
}

func ExampleNew() {
	fmt.Print(newTrip(nil).Mermaid())

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
	printRun(clean, newTrip(nil).Run(ctx, clean))

	failing := &trip{ID: tripID}
	printRun(failing, newTrip(sagas.Permanent(errNoRooms)).Run(ctx, failing))

	// Output:
	// trip-42: completed
	// trip-42: compensated, failed: [hotel], pending: []
}

func ExampleIdempotencyKey() {
	t := &trip{ID: tripID}
	if err := newTrip(nil).Run(context.Background(), t); err != nil {
		panic(err)
	}

	fmt.Println(t.Flight)

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
