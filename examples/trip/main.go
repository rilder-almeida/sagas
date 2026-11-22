// Command trip runs a trip saga once to completion and once to compensation.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/rilder-almeida/sagas"
)

const (
	exitFailure      = 1
	exitWriteFailure = 2
	cardLimit        = 1000
	lisbonRooms      = 2
	lisbonNights     = 3
	portoNights      = 2
	lisbonAmount     = 800
	portoAmount      = 600
)

var (
	errNoRooms   = errors.New("no rooms")
	errOverLimit = errors.New("charge over the card limit")
	errNoAddress = errors.New("empty address")
)

type trip struct {
	ID       string
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

func (t *trip) ExecutionID() string { return t.ID }

type airline struct {
	mu     sync.Mutex
	byKey  map[string]string
	active map[string]bool
}

func newAirline() *airline {
	return &airline{byKey: map[string]string{}, active: map[string]bool{}}
}

func (a *airline) Book(ctx context.Context, key, route string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if id, ok := a.byKey[key]; ok {
		return id, nil
	}

	id := fmt.Sprintf("FL-%s-%d", route, len(a.byKey)+1)
	a.byKey[key] = id
	a.active[id] = true

	return id, nil
}

func (a *airline) Cancel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.active, id)

	return nil
}

type hotels struct {
	mu     sync.Mutex
	free   map[string]int
	byKey  map[string]string
	cities map[string]string
}

func newHotels(free map[string]int) *hotels {
	return &hotels{free: free, byKey: map[string]string{}, cities: map[string]string{}}
}

func (h *hotels) Book(ctx context.Context, key, city string, nights int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if id, ok := h.byKey[key]; ok {
		return id, nil
	}

	if h.free[city] == 0 {
		return "", fmt.Errorf("%w in %s", errNoRooms, city)
	}

	h.free[city]--

	id := fmt.Sprintf("HT-%s-%dn-%d", city, nights, len(h.byKey)+1)
	h.byKey[key] = id
	h.cities[id] = city

	return id, nil
}

func (h *hotels) Cancel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if city, ok := h.cities[id]; ok {
		h.free[city]++
		delete(h.cities, id)
	}

	return nil
}

type payments struct {
	mu    sync.Mutex
	byKey map[string]string
}

func newPayments() *payments { return &payments{byKey: map[string]string{}} }

func (p *payments) Charge(ctx context.Context, key string, amount int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if id, ok := p.byKey[key]; ok {
		return id, nil
	}

	if amount > cardLimit {
		return "", fmt.Errorf("%w: %d above %d", errOverLimit, amount, cardLimit)
	}

	id := fmt.Sprintf("PAY-%d", len(p.byKey)+1)
	p.byKey[key] = id

	return id, nil
}

type mailer struct {
	mu    sync.Mutex
	byKey map[string]string
}

func newMailer() *mailer { return &mailer{byKey: map[string]string{}} }

func (m *mailer) Send(ctx context.Context, key, address string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if id, ok := m.byKey[key]; ok {
		return id, nil
	}

	if address == "" {
		return "", errNoAddress
	}

	id := fmt.Sprintf("MAIL-%d", len(m.byKey)+1)
	m.byKey[key] = id

	return id, nil
}

type flight struct {
	sagas.Step

	airline *airline
}

func (f *flight) Execute(ctx context.Context, t *trip) error {
	id, err := f.airline.Book(ctx, sagas.IdempotencyKey(ctx), t.Route)
	if err != nil {
		return err
	}

	t.FlightID = id

	return nil
}

func (f *flight) Compensate(ctx context.Context, t *trip) error {
	return f.airline.Cancel(ctx, t.FlightID)
}

type hotel struct {
	sagas.Step

	hotels *hotels
}

func (h *hotel) Execute(ctx context.Context, t *trip) error {
	id, err := h.hotels.Book(ctx, sagas.IdempotencyKey(ctx), t.City, t.Nights)
	if errors.Is(err, errNoRooms) {
		return sagas.Permanent(err)
	}

	if err != nil {
		return err
	}

	t.HotelID = id

	return nil
}

func (h *hotel) Compensate(ctx context.Context, t *trip) error {
	return h.hotels.Cancel(ctx, t.HotelID)
}

type charge struct {
	sagas.Step

	payments *payments
}

func (c *charge) Pivot(ctx context.Context, t *trip) error {
	id, err := c.payments.Charge(ctx, sagas.IdempotencyKey(ctx), t.Amount)
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
	if err != nil {
		return err
	}

	t.MailID = id

	return nil
}

func run(w io.Writer) error {
	flow := sagas.Sequence(
		&flight{airline: newAirline()},
		&hotel{hotels: newHotels(map[string]int{"Lisbon": lisbonRooms, "Porto": 0})},
		&charge{payments: newPayments()},
		&email{mailer: newMailer()},
	)

	saga, err := sagas.New[trip]("trip", flow)
	if err != nil {
		return err
	}

	trips := []trip{
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
