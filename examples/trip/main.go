// Command trip runs a trip saga once to completion and once to compensation.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/rilder-almeida/sagas"
)

const (
	booked           = "booked"
	exitFailure      = 1
	exitWriteFailure = 2
)

var errNoRooms = errors.New("no rooms")

type trip struct {
	ID                           string
	RoomsLeft                    int
	Flight, Hotel, Charge, Email string
}

func (t *trip) ExecutionID() string { return t.ID }

type flight struct{ sagas.Step }

func (*flight) Execute(ctx context.Context, t *trip) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t.Flight = sagas.IdempotencyKey(ctx)

	return nil
}

func (*flight) Compensate(ctx context.Context, t *trip) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t.Flight = ""

	return nil
}

type hotel struct{ sagas.Step }

func (*hotel) Execute(ctx context.Context, t *trip) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if t.RoomsLeft == 0 {
		return sagas.Permanent(errNoRooms)
	}

	t.Hotel = booked

	return nil
}

func (*hotel) Compensate(ctx context.Context, t *trip) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t.Hotel = ""

	return nil
}

type charge struct{ sagas.Step }

func (*charge) Pivot(ctx context.Context, t *trip) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t.Charge = "charged"

	return nil
}

type email struct{ sagas.Step }

func (*email) Execute(ctx context.Context, t *trip) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t.Email = "sent"

	return nil
}

func run(w io.Writer) error {
	saga, err := sagas.New[trip]("trip", sagas.Sequence(&flight{}, &hotel{}, &charge{}, &email{}))
	if err != nil {
		return err
	}

	for _, t := range []trip{{ID: "trip-42", RoomsLeft: 1}, {ID: "trip-43", RoomsLeft: 0}} {
		if reportErr := report(w, t.ID, saga.Run(context.Background(), &t)); reportErr != nil {
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

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
