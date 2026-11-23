# How to test a saga

This page answers six questions about tests for your own saga. A saga is a business operation split into steps. Either every step completes, or the library undoes the part that ran. A saga can have one exception, the pivot. The pivot is the step at the point of no return. It applies whole or not at all, so a failed pivot did not apply. After the pivot succeeds, the saga only moves forward. The answers use the `sagastest` package, which turns the rules of the library into test helpers. The [glossary](../glossary.md) defines the terms of the library. The section ["The `sagastest` contract"](../explanation/guarantees.md#the-sagastest-contract) of the guarantees page explains why each helper exists.

The examples test the trip saga of the [tutorial](../tutorial/book-a-trip.md). Its code is in [`examples/trip`](../../examples/trip). In your tests, replace these names with your own:

- `trip` is the type of the state. The state is the value that a run reads and writes. Every method of every step shares it.
- `services` holds the fakes and the flow, and `newServices` makes a fresh one. The flow is the set of steps in their order, which you build with `sagas.Sequence` or `sagas.Parallel`. A fake is a test double of an outside service that keeps its data in memory.
- `newTrips()[0]` is a state value for a run in which every step succeeds.

## How do I write a Setup?

A `Setup` is a function that builds one `sagastest.Case`. A case is one test that a helper runs on the saga. A helper calls the `Setup` once for each case that it runs, so each case gets a fresh saga and fresh fakes. The helper passes its own middleware in the argument `mw`. Middleware is a value that wraps each call of a step. The helpers use it to record the calls and to inject faults. A fault is an error, a lost reply or a panic that a helper forces on one step.

1. Write a function that returns a `sagastest.Setup[S]`, with your state type as `S`.
2. In the `Setup`, make fresh fakes and a fresh flow on each call.
3. Pass `mw` to `sagas.WithMiddleware` in `sagas.New`.
4. Return a `Case` with all five fields:

   ```go
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
   ```

   - `Saga` is the saga under test.
   - `State` is a pointer to the state of the run.
   - `Flow` is the same flow that you gave to `sagas.New`. The helpers read the steps from it.
   - `Check` looks at the fakes after the run and returns an error for an effect that stays behind.
   - `Late` sends the action of a step to the fakes again, with the idempotency key that the helper passes. The action is the method that a step runs forward. An execution is one run of the saga over one state value, and the execution ID names it. The idempotency key is unique for each execution and step. If the library calls the step again or undoes the step, the key stays the same. The step sends the key to the outside service.

5. Write `Check` so that it accepts every end that a helper makes. A helper ends a case in one of three ways:

   - The run is compensated. A compensation is the method `Compensate` that undoes the effect of a step in business terms. It adds a new fact and does not erase the old one. The library runs it for every started step that has one, and only a step before the pivot can have one. In a compensated run, the library undid the steps, and every compensation succeeded.
   - The run completed: each step succeeded or the library skipped it, and the journal recorded the end. The journal is the append-only record of what an execution intends and did. The library writes each intent before the call.
   - The run ended `sagas.ErrIncomplete`. The pivot is the step at the point of no return. It applies whole or not at all, so a failed pivot did not apply. After the pivot succeeds, the saga only moves forward. The run passed the pivot and stopped before the end, or the outcome of the pivot stayed unknown. Unknown means that the pivot call timed out, stopped on a cancel or panicked, so nobody knows whether the pivot applied. A person must finish the run. The library never undoes it.

   The trip `Check` accepts one flight and one room after a charge, and no booking without a charge:

   ```go
   func checkBookings(svc *services, got *trip) error {
   	want := 0
   	if got.ChargeID != "" {
   		want = 1
   	}

   	held, taken := svc.airline.Held(), svc.hotels.Taken()
   	if held != want || taken != want {
   		return fmt.Errorf("%w: flights held %d, rooms taken %d, want %d of each", errLeftBehind, held, taken, want)
   	}

   	return nil
   }
   ```

6. Write `Late` so that it books the named step again with the key. If the step has no late action, return an error:

   ```go
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
   ```

   The code above uses two sentinel errors, fixed values that `errors.Is` finds:

   ```go
   var (
   	errLeftBehind   = errors.New("bookings left behind")
   	errNoLateAction = errors.New("no late action for step")
   )
   ```

## How do I prove the compensations?

Each compensation must be correct in three cases. `sagastest.Compensations` runs each case for each step that has a compensation, and each case must end compensated.

1. Call `sagastest.Compensations` in a test:

   ```go
   func TestCompensations(t *testing.T) {
   	sagastest.Compensations(t, tripSetup(t))
   }
   ```

2. Run the test with `go test`.

The three cases are these:

- In the case "never applied", the action fails before it calls the service. The compensation must find nothing and return nil.
- In the case "compensation runs twice", the action reaches the service and then panics. The compensation then runs twice, and the second run must change nothing.
- In the case "step lands after its compensation", the helper runs the saga to its end and then calls `Late`. The service must refuse the late action, or `Check` must accept it.

An error from `Late` goes to the test log only. `Check` decides the case. With `go test -v`, the trip log shows a line with `case "step lands after its compensation": Late:` for the flight and for the hotel. Each line ends with the error of the service, which refused the late booking.

## How do I prove the pivot and the forward-only steps?

The pivot is the step at the point of no return. It applies whole or not at all, so a failed pivot did not apply. After the pivot succeeds, the saga only moves forward. The forward-only steps come after the pivot. A failure or an unknown outcome of a forward-only step never undoes anything. `sagastest.Pivot` tests both.

1. Call `sagastest.Pivot` in a test:

   ```go
   func TestPivot(t *testing.T) {
   	sagastest.Pivot(t, tripSetup(t))
   }
   ```

2. Run the test with `go test`.

An attempt is one call of an action, of a compensation or of the pivot. `sagastest.Pivot` makes the outcome of the first pivot attempt unknown: the pivot call runs, and then the helper returns a timeout. To the library, the reply looks lost. Then the result depends on the attempts of the pivot:

- If the pivot has two or more attempts, the run must complete. A clean run is a run with no fault. The final state of the run must equal the state of a clean run, by `reflect.DeepEqual`. The `Retry` field of the pivot step sets the number of attempts, three by default. The [retry section](middleware-recipes.md#how-do-i-use-one-retry-setting-on-several-steps) of the middleware page shows the field.
- If the pivot has one attempt, the run must end `sagas.ErrIncomplete`. A pending step is a step that a person still must finish, undo or look into. Here only the unknown pivot is pending, and no compensation runs. The [glossary](../glossary.md) gives every case of a pending step.

`sagastest.Pivot` then runs each forward-only step twice in one run, each step in a fresh case. Each such run must complete. So each step must be idempotent: a second call has the same effect as the first.

## How do I prove my journal?

The journal is the append-only record of what an execution intends and did. The library writes each intent before the call. You pass your own journal to `sagas.New` with `sagas.WithJournal`. `sagastest.Journal` runs the rules that every journal must obey. Your journal implements two interfaces: `sagas.Journal`, which stores events, and `sagas.EventReader`, which reads them back. The contract reads each event back and compares every field with the event that it sent. If the journal holds no event for an execution, `Events` must return an error that wraps `sagas.ErrNoEvents`.

1. Call `sagastest.Journal` in a test with a function that returns a fresh, empty journal on each call. Replace `sagas.NewMemoryJournal` with the constructor of your journal:

   ```go
   func TestJournal(t *testing.T) {
   	sagastest.Journal(t, func() sagas.Journal { return sagas.NewMemoryJournal() })
   }
   ```

2. Run the test with `go test`. Run it again after each upgrade of the library.

## How do I read a failure from a helper?

Each message of a helper starts with `sagastest:`. Most messages name the step and the case. Find the message in this table:

| The message contains | What it means |
|---|---|
| `the clean run failed` | The run with no fault did not complete. Fix the saga or the `Setup` before anything else. |
| `Case.Flow is not a valid flow` | `Flow` does not hold a flow that `sagas.New` accepts. Set it to the flow that you gave to `sagas.New`. |
| `the clean run recorded no call` | The `Setup` did not pass `mw` to `sagas.WithMiddleware`, or the optional `Enabled` method of every step returned false for the state. A step whose `Enabled` method returns false does not run. |
| `ran in the clean run, but Case.Flow does not name it` | `Flow` is not the flow of the saga. |
| `found no step with Compensate` or `found no pivot` | The flow has nothing for this helper to test. |
| `the step did not start` | An earlier step failed, or `Enabled` returned false, so the helper cannot test this step. |
| `Run returned ..., want sagas.ErrCompensated` | The compensation returned an error. A common cause is an error for a step that never applied. |
| `Check:` | `Check` found an effect that stayed behind after the run. |
| `Case.Late is nil` | Set `Late` in the `Case`. |
| `case "unknown outcome": Run returned ..., want nil` | The pivot has two or more attempts, and the run did not complete after a lost reply. |
| `the final state ... differs from the clean run` | A repeat of the pivot changed the state. Make the pivot idempotent with the idempotency key. |
| `want sagas.ErrIncomplete` | The pivot has one attempt, and the run did not end incomplete. |
| `pending steps are ..., want only the pivot` | After an unknown pivot, other steps stayed pending. |
| `a compensation ran after the unknown outcome` | A compensation ran after the outcome of the pivot was unknown. The library must compensate nothing then. |
| `case "step repeated": Run returned ..., want nil` | A forward-only step fails on its second run. Make it idempotent. |
| `journal rule` | Your journal breaks the named rule. The [journal section](../explanation/guarantees.md#the-journal) of the guarantees page describes the rules. |

The message `the clean run recorded no call` has two causes, and it names both. Look at the `Setup` first. Then look at the `Enabled` methods.

## How do I catch a misspelled step method?

`sagas.New` finds the methods of a step by their names: `Execute`, `Pivot`, `Compensate`, `Enabled` and `Wrap`. If a name has a typo, the method is an ordinary method. `sagas.New` returns no error, and the step runs without it. A misspelled `Compensate` turns the compensation off.

`sagas.Inspect` reports each step of a flow as a `sagas.StepReport`. Its fields `Execute`, `Pivot`, `Compensate`, `Enabled` and `Wrap` tell which methods `sagas.New` found.

1. Write a test that compares the method flags of each step with the methods that you wrote:

   ```go
   func TestStepMethods(t *testing.T) {
   	steps, err := sagas.Inspect[trip](newServices().flow)
   	if err != nil {
   		t.Fatal(err)
   	}

   	type methods struct{ Execute, Pivot, Compensate, Enabled, Wrap bool }

   	want := map[string]methods{
   		"flight": {Execute: true, Compensate: true},
   		"hotel":  {Execute: true, Compensate: true},
   		"charge": {Pivot: true},
   		"email":  {Execute: true},
   	}

   	if len(steps) != len(want) {
   		t.Errorf("the flow has %d steps, want %d", len(steps), len(want))
   	}

   	for _, s := range steps {
   		got := methods{s.Execute, s.Pivot, s.Compensate, s.Enabled, s.Wrap}
   		if got != want[s.Name] {
   			t.Errorf("step %q has methods %+v, want %+v", s.Name, got, want[s.Name])
   		}
   	}
   }
   ```

2. Run the test with `go test`.

If the `hotel` type has a method `Compenstate` in place of `Compensate`, the test fails. The message starts with `step "hotel" has methods`. It shows the flags that `sagas.Inspect` found, with `Compensate:false`, and then the flags that you want, with `Compensate:true`.
