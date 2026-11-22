package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const cardLimit = 1000

var (
	errNoRooms   = errors.New("no rooms")
	errOverLimit = errors.New("charge over the card limit")
	errNoAddress = errors.New("empty address")
)

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

func (a *airline) Booked(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.active[id]
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
