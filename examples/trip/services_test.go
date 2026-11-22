package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func canceledContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	return ctx
}

func callEach(keys []string, call func(key string) (string, error)) ([]string, error) {
	ids := make([]string, 0, len(keys))

	var err error

	for _, key := range keys {
		var id string

		id, err = call(key)
		ids = append(ids, id)
	}

	return ids, err
}

func assertCalls(t *testing.T, ids []string, err error, wantDistinct int, wantErr error) {
	t.Helper()

	requireErr(t, err, wantErr)
	assert.Equal(t, wantDistinct, distinct(ids))
}

func distinct(ids []string) int {
	seen := map[string]bool{}

	for _, id := range ids {
		if id != "" {
			seen[id] = true
		}
	}

	return len(seen)
}

func requireErr(t *testing.T, err, want error) {
	t.Helper()

	if want != nil {
		require.ErrorIs(t, err, want)
	} else {
		require.NoError(t, err)
	}
}

func Test_hotels_Book(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		free         int
		keys         []string
		canceled     bool
		wantDistinct int
		wantLeft     int
		wantErr      error
	}{
		{
			name:         "[Success] When: the same key books twice ; Should: return one ID and take one room",
			free:         2,
			keys:         []string{"k1", "k1"},
			wantDistinct: 1,
			wantLeft:     1,
		},
		{
			name:         "[Success] When: two keys book ; Should: return two IDs and take two rooms",
			free:         2,
			keys:         []string{"k1", "k2"},
			wantDistinct: 2,
		},
		{
			name:    "[Fail] When: the city has no rooms ; Should: return errNoRooms",
			keys:    []string{"k1"},
			wantErr: errNoRooms,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error and take no room",
			free:     1,
			keys:     []string{"k1"},
			canceled: true,
			wantLeft: 1,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newHotels(map[string]int{"Lisbon": tt.free})

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			ids, err := callEach(tt.keys, func(key string) (string, error) { return svc.Book(ctx, key, "Lisbon", 1) })

			assertCalls(t, ids, err, tt.wantDistinct, tt.wantErr)
			assert.Equal(t, tt.wantLeft, svc.Free("Lisbon"))
		})
	}
}

func Test_payments_Charge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		amount       int
		keys         []string
		canceled     bool
		wantDistinct int
		wantErr      error
	}{
		{
			name:         "[Success] When: the same key charges twice ; Should: return one ID",
			amount:       cardLimit,
			keys:         []string{"k1", "k1"},
			wantDistinct: 1,
		},
		{
			name:         "[Success] When: two keys charge ; Should: return two IDs",
			amount:       cardLimit,
			keys:         []string{"k1", "k2"},
			wantDistinct: 2,
		},
		{
			name:    "[Fail] When: the amount is above the limit ; Should: return errOverLimit",
			amount:  cardLimit + 1,
			keys:    []string{"k1"},
			wantErr: errOverLimit,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error",
			amount:   1,
			keys:     []string{"k1"},
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newPayments()

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			ids, err := callEach(tt.keys, func(key string) (string, error) { return svc.Charge(ctx, key, tt.amount) })

			assertCalls(t, ids, err, tt.wantDistinct, tt.wantErr)
		})
	}
}

func Test_mailer_Send(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		address      string
		keys         []string
		canceled     bool
		wantDistinct int
		wantErr      error
	}{
		{
			name:         "[Success] When: one key sends three times ; Should: return one ID",
			address:      "ana@example.com",
			keys:         []string{"k1", "k1", "k1"},
			wantDistinct: 1,
		},
		{
			name:         "[Success] When: two keys send ; Should: return two IDs",
			address:      "ana@example.com",
			keys:         []string{"k1", "k2"},
			wantDistinct: 2,
		},
		{
			name:    "[Fail] When: the address is empty ; Should: return errNoAddress",
			keys:    []string{"k1"},
			wantErr: errNoAddress,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error",
			address:  "ana@example.com",
			keys:     []string{"k1"},
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newMailer()

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			ids, err := callEach(tt.keys, func(key string) (string, error) { return svc.Send(ctx, key, tt.address) })

			assertCalls(t, ids, err, tt.wantDistinct, tt.wantErr)
		})
	}
}

func Test_airline_Book(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		keys         []string
		canceled     bool
		wantDistinct int
		wantErr      error
	}{
		{
			name:         "[Success] When: the same key books twice ; Should: return one ID",
			keys:         []string{"k1", "k1"},
			wantDistinct: 1,
		},
		{
			name:         "[Success] When: two keys book ; Should: return two IDs",
			keys:         []string{"k1", "k2"},
			wantDistinct: 2,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error and book nothing",
			keys:     []string{"k1"},
			canceled: true,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newAirline()

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			ids, err := callEach(tt.keys, func(key string) (string, error) { return svc.Book(ctx, key, "GRU-LIS") })

			assertCalls(t, ids, err, tt.wantDistinct, tt.wantErr)
		})
	}
}

func Test_hotels_Cancel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		book        bool
		cancels     int
		canceled    bool
		wantLeft    int
		wantErr     error
		wantLateErr error
	}{
		{
			name:        "[Success] When: a booked key is canceled twice ; Should: free one room and refuse the key",
			book:        true,
			cancels:     2,
			wantLeft:    2,
			wantLateErr: errCanceled,
		},
		{
			name:        "[Fail] When: an unknown key is canceled ; Should: return errNotFound and refuse the key",
			cancels:     1,
			wantLeft:    2,
			wantErr:     errNotFound,
			wantLateErr: errCanceled,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error and keep the room",
			book:     true,
			cancels:  1,
			canceled: true,
			wantLeft: 1,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newHotels(map[string]int{"Lisbon": 2})

			if tt.book {
				_, err := svc.Book(t.Context(), "k1", "Lisbon", 1)
				require.NoError(t, err)
			}

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			var err error
			for range tt.cancels {
				err = svc.Cancel(ctx, "k1")
			}

			requireErr(t, err, tt.wantErr)

			_, lateErr := svc.Book(t.Context(), "k1", "Lisbon", 1)

			requireErr(t, lateErr, tt.wantLateErr)
			assert.Equal(t, tt.wantLeft, svc.Free("Lisbon"))
		})
	}
}

func Test_airline_Cancel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		book        bool
		cancels     int
		canceled    bool
		wantHeld    int
		wantErr     error
		wantLateErr error
	}{
		{
			name:        "[Success] When: a booked key is canceled twice ; Should: hold no flight and refuse the key",
			book:        true,
			cancels:     2,
			wantLateErr: errCanceled,
		},
		{
			name:        "[Fail] When: an unknown key is canceled ; Should: return errNotFound and refuse the key",
			cancels:     1,
			wantErr:     errNotFound,
			wantLateErr: errCanceled,
		},
		{
			name:     "[Fail] When: the context is done ; Should: return its error and keep the flight",
			book:     true,
			cancels:  1,
			canceled: true,
			wantHeld: 1,
			wantErr:  context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newAirline()

			if tt.book {
				_, err := svc.Book(t.Context(), "k1", "GRU-LIS")
				require.NoError(t, err)
			}

			ctx := t.Context()
			if tt.canceled {
				ctx = canceledContext(t)
			}

			var err error
			for range tt.cancels {
				err = svc.Cancel(ctx, "k1")
			}

			requireErr(t, err, tt.wantErr)

			_, lateErr := svc.Book(t.Context(), "k1", "GRU-LIS")

			requireErr(t, lateErr, tt.wantLateErr)
			assert.Equal(t, tt.wantHeld, svc.Held())
		})
	}
}
