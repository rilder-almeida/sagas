package sagas_test

import (
	"context"
	"errors"
	"fmt"
	"io"
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

func ExampleNew() {
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
