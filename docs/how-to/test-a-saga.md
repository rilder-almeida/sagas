# How to test a saga

This page answers six questions about tests for your own saga. The answers use the `sagastest` package, which turns the rules of the library into test helpers. The [glossary](../glossary.md) defines the terms of the library. The section ["The `sagastest` contract"](../explanation/guarantees.md#the-sagastest-contract) of the guarantees page explains why each helper exists.

The examples test the trip saga of the [tutorial](../tutorial/book-a-trip.md). Its code is in [`examples/trip`](../../examples/trip). In your tests, replace these names with your own:

- `trip` is the type of the state.
- `services` holds the fakes and the flow, and `newServices` makes a fresh one. A fake is a test double of an outside service that keeps its data in memory.
- `newTrips()[0]` is a state value for a run that completes.

## How do I write a Setup?

A `Setup` is a function that builds one `sagastest.Case`. A helper calls it once for each case that it runs, so each case gets a fresh saga and fresh fakes. The helper passes its own middleware in the argument `mw`. Middleware is a value that wraps each call of a step. The helpers use it to record the calls and to inject faults. A fault is an error, a lost reply or a panic that a helper forces on one step.

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
   - `Late` sends the action of a step to the fakes again, with the idempotency key that the helper passes.

5. Write `Check` so that it accepts every end that a helper makes. A helper ends a case in one of three ways. The run is compensated, it completed, or it ended `sagas.ErrIncomplete` after the pivot. The trip `Check` accepts one flight and one room after a charge, and no booking without a charge:

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

   The two errors of the trip tests are sentinel errors, fixed values that `errors.Is` finds:

   ```go
   var (
   	errLeftBehind   = errors.New("bookings left behind")
   	errNoLateAction = errors.New("no late action for step")
   )
   ```

## How do I prove the compensations?

A compensation is the method `Compensate` that undoes a step. It must be correct in three cases. `sagastest.Compensations` runs each case for each step that has a compensation, and each case must end compensated.

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

An error from `Late` goes to the test log only. `Check` decides the case. With `go test -v`, the trip log shows `Late: booking key already canceled`, because the service refused the late booking.

## How do I prove the pivot and the forward-only steps?

The pivot is the step after which the saga only moves forward. The forward-only steps come after the pivot. `sagastest.Pivot` tests both.

1. Call `sagastest.Pivot` in a test:

   ```go
   func TestPivot(t *testing.T) {
   	sagastest.Pivot(t, tripSetup(t))
   }
   ```

2. Run the test with `go test`.

`Pivot` makes the outcome of the first pivot attempt unknown: the pivot call runs, and its reply gets lost. Then the result depends on the attempts of the pivot:

- If the pivot has two or more attempts, the run must complete. The `Retry` field of the pivot step sets the number of attempts, three by default. The [retry section](middleware-recipes.md#how-do-i-use-one-retry-setting-on-several-steps) of the middleware page shows the field. Its final state must equal the state of a clean run, by `reflect.DeepEqual`.
- If the pivot has one attempt, the run must end `sagas.ErrIncomplete`. Only the pivot is pending, and no compensation runs.

`Pivot` then runs each forward-only step twice in one run, each step in a fresh case. Each such run must complete. So each step must be idempotent: a second call has the same effect as the first.

## How do I prove my journal?

A journal is the append-only record of an execution. You pass your own journal to `sagas.New` with `sagas.WithJournal`. `sagastest.Journal` runs the rules that every journal must obey.

1. Call `sagastest.Journal` in a test with a function that returns a fresh, empty journal on each call. Replace `sagas.NewMemoryJournal` with the constructor of your journal:

   ```go
   func TestJournal(t *testing.T) {
   	sagastest.Journal(t, func() sagas.Journal { return sagas.NewMemoryJournal() })
   }
   ```

2. Run the test with `go test`.

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

If the `hotel` type has a method `Compenstate` in place of `Compensate`, the test fails with this message:

```
step "hotel" has methods {Execute:true Pivot:false Compensate:false Enabled:false Wrap:false}, want {Execute:true Pivot:false Compensate:true Enabled:false Wrap:false}
```
