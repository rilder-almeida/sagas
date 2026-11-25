package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/rilder-almeida/sagas"
)

const (
	exitFailure      = 1
	exitWriteFailure = 2
)

// --8<-- [start:state]

type trip struct {
	ID       string `sagas:"id"`
	Route    string
	City     string
	Nights   int
	Amount   int
	Email    string
	FlightID string
	HotelID  string
	ChargeID string
	MailID   string
}

// --8<-- [end:state]

// --8<-- [start:steps]

type flight struct {
	sagas.Step

	airline *airline
}

func (f *flight) Execute(ctx context.Context, t *trip) error {
	id, err := f.airline.Book(ctx, sagas.IdempotencyKey(ctx), t.Route)
	if errors.Is(err, errCanceled) {
		return sagas.Permanent(err)
	}

	if err != nil {
		return err
	}

	t.FlightID = id

	return nil
}

func (f *flight) Compensate(ctx context.Context, _ *trip) error {
	err := f.airline.Cancel(ctx, sagas.IdempotencyKey(ctx))
	if errors.Is(err, errNotFound) {
		return nil
	}

	return err
}

type hotel struct {
	sagas.Step

	hotels *hotels
}

func (h *hotel) Execute(ctx context.Context, t *trip) error {
	id, err := h.hotels.Book(ctx, sagas.IdempotencyKey(ctx), t.City, t.Nights)
	if errors.Is(err, errNoRooms) || errors.Is(err, errCanceled) {
		return sagas.Permanent(err)
	}

	if err != nil {
		return err
	}

	t.HotelID = id

	return nil
}

func (h *hotel) Compensate(ctx context.Context, _ *trip) error {
	err := h.hotels.Cancel(ctx, sagas.IdempotencyKey(ctx))
	if errors.Is(err, errNotFound) {
		return nil
	}

	return err
}

type charge struct {
	sagas.Step

	payments *payments
}

func (c *charge) Pivot(ctx context.Context, t *trip) error {
	id, err := c.payments.Charge(ctx, sagas.IdempotencyKey(ctx), t.Amount)
	if errors.Is(err, errOverLimit) {
		return sagas.Permanent(err)
	}

	if err != nil {
		return err
	}

	t.ChargeID = id

	return nil
}

type email struct {
	sagas.Step

	mailer *mailer
}

func (e *email) Execute(ctx context.Context, t *trip) error {
	id, err := e.mailer.Send(ctx, sagas.IdempotencyKey(ctx), t.Email)
	if errors.Is(err, errNoAddress) {
		return sagas.Permanent(err)
	}

	if err != nil {
		return err
	}

	t.MailID = id

	return nil
}

// --8<-- [end:steps]

// --8<-- [start:saga]

const lisbonRooms = 2

type services struct {
	airline  *airline
	hotels   *hotels
	payments *payments
	mailer   *mailer
	flow     sagas.Flow
}

func newServices() *services {
	s := &services{
		airline:  newAirline(),
		hotels:   newHotels(map[string]int{"Lisbon": lisbonRooms, "Porto": 0}),
		payments: newPayments(),
		mailer:   newMailer(),
	}

	s.flow = sagas.Sequence(
		&flight{airline: s.airline},
		&hotel{hotels: s.hotels},
		&charge{payments: s.payments},
		&email{mailer: s.mailer},
	)

	return s
}

// --8<-- [end:saga]

// --8<-- [start:run]

const (
	lisbonNights = 3
	portoNights  = 2
	lisbonAmount = 800
	portoAmount  = 600
)

func newTrips() []trip {
	return []trip{
		{
			ID:     "trip-42",
			Route:  "GRU-LIS",
			City:   "Lisbon",
			Nights: lisbonNights,
			Amount: lisbonAmount,
			Email:  "ana@example.com",
		},
		{
			ID:     "trip-43",
			Route:  "GRU-OPO",
			City:   "Porto",
			Nights: portoNights,
			Amount: portoAmount,
			Email:  "bia@example.com",
		},
	}
}

func run(w io.Writer) error {
	saga, err := sagas.New[trip]("trip", newServices().flow)
	if err != nil {
		return err
	}

	trips := newTrips()

	for i := range trips {
		if reportErr := report(w, trips[i].ID, saga.Run(context.Background(), &trips[i])); reportErr != nil {
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
