# Tutorial: book a trip with a saga

In this tutorial, you build a small program that books a trip. It books a flight and a hotel, charges a card and sends an email. Then you break the program on purpose, and you watch the library undo the bookings.

You need Go 1.27 or later. You do not need to know about sagas. This page explains each term where it first appears, and the [glossary](../glossary.md) defines every term of the library.

## What you build

A saga is a business operation split into steps. Either every step completes, or the library undoes the part that ran. A step undoes its work with a compensation. A compensation is a method that cancels the effect of the step in business terms.

The trip saga has four steps, in this order:

1. `flight` books a seat. Its compensation cancels the seat.
2. `hotel` books a room. Its compensation cancels the room.
3. `charge` charges the card. You cannot take a charge back, so this step is the pivot. The pivot is the point of no return. After the pivot succeeds, the saga only moves forward.
4. `email` sends a confirmation. It comes after the pivot, so it has no compensation.

The finished program is in the folder [`examples/trip`](../../examples/trip) of the repository. Each code block on this page comes from it.

## Step 1: make the module

1. Make a folder and a Go module, and add the library:

   ```sh
   mkdir trip
   cd trip
   go mod init example.com/trip
   go get github.com/rilder-almeida/sagas
   ```

2. Copy the file [`examples/trip/services.go`](../../examples/trip/services.go) from the repository into the `trip` folder.

The file holds four simulated services: `airline`, `hotels`, `payments` and `mailer`. They stand in for real services and keep their data in memory. You need four facts about them:

- Each booking call takes a key. A second call with the same key returns the first booking and books nothing new.
- If the city has no free room, `hotels.Book` returns `errNoRooms`.
- `airline.Cancel` and `hotels.Cancel` cancel the booking of a key. For a key that never booked, they return `errNotFound`.
- After a cancel, a booking call with the same key returns `errCanceled`.

`airline.Held` returns the number of booked flights. `hotels.Free` returns the number of free rooms in a city. The tests at the end of this tutorial use both.

## Step 2: write the state

The state is the value that every step reads and writes. Make a file `trip.go` with this code:

```go
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
	lisbonRooms      = 2
	lisbonNights     = 3
	portoNights      = 2
	lisbonAmount     = 800
	portoAmount      = 600
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
```

One run of the saga over one state value is an execution. The `ExecutionID` method gives the execution a name. Here the name is the trip ID, so two requests for `trip-42` are the same execution.

## Step 3: write the steps

A step is a type that embeds `sagas.Step`. Its `Execute` method is the action, the work that the step does forward. The library takes the name of the step from the name of the type. Add the `flight` step to `trip.go`:

```go
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

func (f *flight) Compensate(ctx context.Context, _ *trip) error {
	err := f.airline.Cancel(ctx, sagas.IdempotencyKey(ctx))
	if errors.Is(err, errNotFound) {
		return nil
	}

	return err
}
```

The library can call a method more than once. An attempt is one call. After a timeout, for example, the library makes another attempt. `sagas.IdempotencyKey(ctx)` returns the idempotency key, a key that is the same on every attempt of the step and on its compensation. The airline uses the key to book only once.

The `Compensate` method is the compensation. It cancels by the key, not by the ID in the state. If the reply of the airline gets lost, the state holds no ID, but the key still finds the booking. If the airline answers `errNotFound`, the flight never booked. Nothing needs a cancel, so the compensation returns nil, which means success.

Add the `hotel` step. It works like `flight`:

```go
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

func (h *hotel) Compensate(ctx context.Context, _ *trip) error {
	err := h.hotels.Cancel(ctx, sagas.IdempotencyKey(ctx))
	if errors.Is(err, errNotFound) {
		return nil
	}

	return err
}
```

`sagas.Permanent(err)` marks an error that stops the attempts at once. A city with no free room now has no free room on the next attempt either.

Add the `charge` step. Its method is `Pivot`, not `Execute`, so this step is the pivot. A pivot has no compensation:

```go
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
```

Add the `email` step:

```go
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
```

## Step 4: put the steps in order

A flow is the order of the steps. `sagas.Sequence` makes a flow that runs its steps one after another. Add a type that holds the services and the flow, and a function that makes two trips:

```go
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
```

Lisbon has two free rooms. Porto has none, so the second trip cannot get a hotel.

## Step 5: run the saga

`sagas.New` takes the flow, rejects a flow with an error in it, and returns the saga. `Run` runs one execution. Add the code that runs both trips and prints how each run ended:

```go
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
```

If every step completed, `Run` returns nil. Otherwise it returns a `*sagas.RunError`. Its `Kind` names how the run ended. Its `Pending` lists the steps that a person still must finish.

Make a file `main.go`:

```go
// Command trip runs a trip saga once to completion and once to compensation.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
```

Run the program:

```sh
go run .
```

You see this output:

```
trip-42: completed
trip-43: compensated, pending: []
```

The first trip completed. The second trip went to Porto, where the hotel step failed with `errNoRooms`. The library then ran the compensations of the started steps in reverse order: first the hotel, then the flight. The run ended compensated, which means that every compensation succeeded. No step is pending.

## Step 6: break a step on purpose

The `sagastest` package holds test helpers. Some of them inject a fault: they make one step fail, lose its reply or crash. Each fault is middleware. Middleware is a value that wraps each call of a step, and you give it to `sagas.New` with `sagas.WithMiddleware`.

Make a file `faults_test.go`. The function `tryTrip` runs the first trip with the faults that you pass. A `Recorder` from `sagastest` records each call. The function prints how the run ended, each call, and the number of booked flights:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/rilder-almeida/sagas"
	"github.com/rilder-almeida/sagas/sagastest"
)

func tryTrip(faults ...sagas.Middleware) {
	rec := sagastest.NewRecorder()
	mw := append([]sagas.Middleware{rec.Middleware()}, faults...)
	svc := newServices()

	saga, err := sagas.New[trip]("trip", svc.flow, sagas.WithMiddleware(mw...))
	if err != nil {
		fmt.Println(err)

		return
	}

	state := newTrips()[0]
	if err := report(os.Stdout, state.ID, saga.Run(context.Background(), &state)); err != nil {
		fmt.Println(err)
	}

	for _, call := range rec.Calls() {
		fmt.Println(call.Step, call.Phase, call.Attempt, call.Err)
	}

	fmt.Println("flights held:", svc.airline.Held())
}

func Example_clean() {
	tryTrip()
	// Output:
	// trip-42: completed
	// flight execute 1 <nil>
	// hotel execute 1 <nil>
	// charge pivot 1 <nil>
	// email execute 1 <nil>
	// flights held: 1
}
```

Each line of a call shows the step, the phase, the attempt number and the error. The phase names the method that ran: `execute`, `pivot` or `compensate`. `<nil>` means that the call succeeded.

`Example_clean` is a Go example. `go test` runs it and compares what it prints with the `// Output:` comment. If the two differ, the test fails and shows both. Run the tests:

```sh
go test .
```

The output starts with `ok`. This clean run injects no fault, so it is the passing call that the next three examples break.

### Make the hotel fail

`sagastest.Fail` returns an error from a step without calling the step. `sagastest.OnPhase(sagas.PhaseExecute)` limits the fault to the action, so the compensation of the hotel still runs. Add this example to `faults_test.go`:

```go
func Example_hotelDown() {
	tryTrip(sagastest.Fail("hotel", errors.New("hotel service down"), sagastest.OnPhase(sagas.PhaseExecute)))
	// Output:
	// trip-42: compensated, pending: []
	// flight execute 1 <nil>
	// hotel execute 1 hotel service down
	// hotel execute 2 hotel service down
	// hotel execute 3 hotel service down
	// hotel compensate 1 <nil>
	// flight compensate 1 <nil>
	// flights held: 0
}
```

The hotel step failed three times, because a step makes three attempts by default. The library then compensated the hotel and the flight, in reverse order. The hotel never booked, so its compensation got `errNotFound` and returned success. The flight is canceled, and no flight is held.

### Lose the reply of the flight

`sagastest.Unknown` calls the step. Then it returns `context.DeadlineExceeded`, so the reply looks lost. The booking landed, but the saga does not know it. Such an outcome is unknown. `sagastest.OnAttempts(1)` limits the fault to the first attempt:

```go
func Example_lostReply() {
	tryTrip(sagastest.Unknown("flight", sagastest.OnAttempts(1)))
	// Output:
	// trip-42: completed
	// flight execute 1 context deadline exceeded
	// flight execute 2 <nil>
	// hotel execute 1 <nil>
	// charge pivot 1 <nil>
	// email execute 1 <nil>
	// flights held: 1
}
```

The second attempt sent the same idempotency key. The airline returned the first booking and booked nothing new, so one flight is held.

### Crash the email step

`sagastest.Panic` calls the step and then panics. The email went out, and the saga does not know it. The email step comes after the pivot, and the library never undoes a step there:

```go
func Example_crash() {
	tryTrip(sagastest.Panic("email"))
	// Output:
	// trip-42: incomplete, pending: [email]
	// flight execute 1 <nil>
	// hotel execute 1 <nil>
	// charge pivot 1 <nil>
	// email execute 1 sagas: panic: sagastest: injected panic in step "email"
	// flights held: 1
}
```

A panic stops the attempts at once, because a retry only repeats a code defect. The run ended incomplete, which means that it passed the pivot and stopped before the end. The `email` step is pending. A person must find out whether the email went out.

Run `go test .` again. The output starts with `ok`.

## Step 7: prove the compensations

A compensation must be correct in three cases: the action never applied, the compensation runs twice, and the action lands after its compensation. `sagastest.Compensations` runs each case for each step that has a compensation. `sagastest.Pivot` gives the pivot a lost reply, and repeats each step after the pivot.

Both helpers take a `Setup`. A `Setup` is a function that builds a fresh saga and fresh services for each case. It returns a `sagastest.Case` with five fields:

- `Saga` is the saga. It must get the middleware `mw` of the helper through `sagas.WithMiddleware`.
- `State` is the state of the run.
- `Flow` is the flow that the `Setup` gave to `sagas.New`.
- `Check` looks at the services after the run. If a booking stays behind, it returns an error.
- `Late` books a step again with the key that the helper passes. The helper uses it for the case where the action lands after its compensation.

1. Add testify, a test library. The `Setup` uses it to stop the test on an error:

   ```sh
   go get github.com/stretchr/testify
   ```

2. Make a file `trip_test.go` with this code:

   ```go
   package main

   import (
   	"context"
   	"errors"
   	"fmt"
   	"testing"

   	"github.com/rilder-almeida/sagas"
   	"github.com/rilder-almeida/sagas/sagastest"
   	"github.com/stretchr/testify/require"
   )

   var (
   	errLeftBehind   = errors.New("bookings left behind")
   	errNoLateAction = errors.New("no late action for step")
   )

   func checkBookings(svc *services, got *trip) error {
   	want := 0
   	if got.ChargeID != "" {
   		want = 1
   	}

   	held, taken := svc.airline.Held(), lisbonRooms-svc.hotels.Free("Lisbon")
   	if held != want || taken != want {
   		return fmt.Errorf("%w: flights held %d, rooms taken %d, want %d of each", errLeftBehind, held, taken, want)
   	}

   	return nil
   }

   func bookLate(ctx context.Context, svc *services, t *trip, step, key string) error {
   	switch step {
   	case "flight":
   		_, err := svc.airline.Book(ctx, key, t.Route)

   		return err
   	case "hotel":
   		_, err := svc.hotels.Book(ctx, key, t.City, t.Nights)

   		return err
   	default:
   		return fmt.Errorf("%w: %q", errNoLateAction, step)
   	}
   }

   func tripSetup(t *testing.T) sagastest.Setup[trip] {
   	t.Helper()

   	return func(mw ...sagas.Middleware) sagastest.Case[trip] {
   		svc := newServices()

   		saga, err := sagas.New[trip]("trip", svc.flow, sagas.WithMiddleware(mw...))
   		require.NoError(t, err)

   		state := &newTrips()[0]

   		return sagastest.Case[trip]{
   			Saga:  saga,
   			State: state,
   			Flow:  svc.flow,
   			Check: func(got *trip) error { return checkBookings(svc, got) },
   			Late:  func(ctx context.Context, step, key string) error { return bookLate(ctx, svc, state, step, key) },
   		}
   	}
   }

   func TestCompensations(t *testing.T) {
   	sagastest.Compensations(t, tripSetup(t))
   }

   func TestPivot(t *testing.T) {
   	sagastest.Pivot(t, tripSetup(t))
   }
   ```

3. Run the tests:

   ```sh
   go test .
   ```

The output starts with `ok`. If the card was charged, `Check` accepts one flight and one room. Otherwise, it accepts none, so a compensated run must leave no booking behind.

If you run `go test -v .`, the log shows two lines that contain `Late: booking key already canceled`. These lines are no fault. The late booking came after the compensation, and the service refused the canceled key. That is the correct answer.

## Step 8: watch a test catch a bad compensation

Now you break the compensation of the flight and watch `Compensations` find it.

1. In `trip.go`, change the `Compensate` method of `flight` so that it returns the error of the cancel as it is:

   ```go
   func (f *flight) Compensate(ctx context.Context, _ *trip) error {
   	return f.airline.Cancel(ctx, sagas.IdempotencyKey(ctx))
   }
   ```

2. Run the tests:

   ```sh
   go test -run TestCompensations .
   ```

The test fails. After the file name and the line number, the first message starts with this text:

```
sagastest: step "flight", case "never applied": Run returned sagas: compensation failed:
```

In the case "never applied", the flight never booked. The airline answered `errNotFound`, and the compensation returned that error three times. The run ended `compensation failed` instead of compensated. In production, a person then has to finish an undo that had nothing to undo.

3. Undo your change in `trip.go`, and run `go test .` again. The output starts with `ok`.

## What you learned

You built a saga with a pivot, and you ran it to completion and to compensation. You injected a failure, a lost reply and a panic, and you saw how the library answered each one. You proved the compensations and the pivot with `sagastest`.

Next, read these pages:

- [How to test a saga](../how-to/test-a-saga.md) shows each helper of `sagastest` on your own saga.
- [Middleware recipes and run settings](../how-to/middleware-recipes.md) shows how to limit, retry and time-bound the calls.
- [The guarantees of a saga](../explanation/guarantees.md) explains why the library works this way.
