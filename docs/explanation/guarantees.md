# The guarantees of a saga

This page explains what the library promises, what it leaves to you, and why. Read it to understand the contract. For the steps to follow, see the tutorial and the how-to guides.

The ideas come from four sources:

- [Garcia-Molina and Salem, "Sagas" (1987)](https://www.cs.cornell.edu/andru/cs711/2002fa/reading/sagas.pdf) introduce the saga and its compensations.
- [McCaffrey, Kingsbury and Narula, "Distributed sagas" (2016)](https://github.com/aphyr/dist-sagas/blob/master/sagas.pdf) treat the unknown outcome of a call as a normal case.
- [Sookocheff, "Fault tolerance and data consistency using distributed sagas" (2018)](https://sookocheff.com/post/architecture/fault-tolerance-and-data-consistency-using-distributed-sagas/) describes the pattern for services.
- Mehrotra and others (1992) give the terms pivot, compensable and retriable, which this library maps to the pivot, the compensable zone and the forward-only zone.

## What is all-or-nothing

A saga is not a database transaction. No lock holds the steps together, and other readers see the effect of each step as soon as it ends. The promise is smaller: when the saga ends, either every step is done or the library has undone the part that was done.

The library keeps this promise in three ways.

1. It compensates every step that started. A step that failed, timed out or was canceled can still change something outside. A charge that the bank confirms after your timeout is the usual example. A library that compensates only completed steps never refunds that charge.
2. It stops the running steps before it compensates. On the first failure, it cancels the context of the running steps and starts nothing new. It waits for every step to return and only then starts the compensations. A branch that compensates while a sibling still writes the state mixes the two phases in the journal. `Run` never returns while a goroutine that it started is alive.
3. It compensates along the reversed graph. A compensation waits for the compensations of every started step that depended on it. Then it starts. Steps that do not depend on each other compensate in parallel. Only the graph decides the order, forward and backward. If a business order matters, write it into the flow.

A step that the flow skips never started. The library does not compensate it, and it counts as done for the steps that follow.

## The pivot

Some steps cannot be undone. A payment that left the bank, an email that went out, a parcel that shipped: a compensation for these steps is only a new fact, and sometimes there is none. The pivot is the step where the saga passes the point of no return.

A pivot splits the saga into two zones:

- The compensable zone holds the steps before the pivot. A failure here undoes them.
- The forward-only zone holds the steps after the pivot. A failure here never undoes anything.

`New` rejects a saga that breaks one of these rules. A saga has at most one pivot. Every other step comes before the pivot or after it in the flow. The pivot has no `Compensate` and no `Enabled`. No step after the pivot has `Compensate`. The error comes from `New`, not from a surprise at run time.

At run time, an error from the pivot means that it did not apply, so the library undoes the compensable zone. An unknown outcome from the pivot is different. The library retries it with the same idempotency key and never undoes it. When the attempts run out, the run ends `ErrIncomplete` with only the pivot pending, and nothing is compensated. A failure after the pivot also ends `ErrIncomplete`, because the saga must finish and cannot go back.

The pivot call runs in one retry loop on a context that the caller's cancel does not stop. The reason is that a cancel half way through a pivot leaves its outcome unknown. The grace period below bounds that loop.

If the pivot call succeeds and its commit fails, the library counts the pivot as unknown. It commits nothing and compensates nothing. The run ends `ErrIncomplete` with the pivot in `Pending` and `ErrInvalidState` in its error. The other choice, to count the pivot as failed, compensates around a pivot that did apply.

## At-least-once and idempotency

The library calls each action and each compensation at least once. It can call one twice. A retry after a timeout, a resend after a journal fault and a compensation after an unknown outcome all repeat a call. The library cannot know whether the first call changed anything outside, so it asks you for one thing in return: make every action and every compensation idempotent.

A call that runs twice has the same effect as a call that runs once. This is idempotency. The library helps with a key. In a step method, `sagas.IdempotencyKey(ctx)` returns the execution ID, a slash and the step name. The key is the same on every attempt, on the compensation and after a resume. Pass it to the outside service, and the service can recognize a repeat:

```go
func (c *charge) Pivot(ctx context.Context, b *booking) error {
	id, err := c.payments.Charge(ctx, sagas.IdempotencyKey(ctx), b.Amount)
	if err != nil {
		return err
	}

	b.PaymentID = id

	return nil
}
```

In middleware, the same key is `info.IdempotencyKey()`. The key holds the execution ID. If the ID changes between requests, the key changes too. Give the state an `ExecutionID() string` method that returns a business ID, such as an order number. Without it, the library draws a random ID, and a repeated request looks like a new execution.

### Retries

A step retries its action and its compensation under one policy. The default is three attempts with an exponential delay and jitter. Set `Retry` on the step to change it. A step with `sagas.NoRetry()` makes one attempt, and its compensation also gets one attempt. A caller that wants a more persistent compensation raises `Retry.Attempts`. `RecoveryTimeout` bounds the whole recovery.

The retries stop at the first of these: the attempts run out, the call returns an error that carries `sagas.Permanent`, the call panics, or the deadline passes. The `Permanent` mark counts at any depth of the error chain, so wrapping it with `%w` keeps it. A panic is a code defect, and a retry only repeats it.

The library does not limit how many steps run at once or how fast. That is load control, and it varies with each application. You write it as middleware.

## The three compensation cases

A compensation must be correct in three cases. The library treats an unknown outcome as normal, so each case can happen in production.

1. The step never applied. The call timed out, was canceled or failed before it changed anything, and the compensation still runs. It must do nothing and return success.
2. The compensation runs twice. A retry or a resend repeats it. The second run must have the same effect as the first.
3. The step lands after its compensation. A slow call that the library gave up on can still reach the outside service later. The compensation must tolerate this, usually because the service refuses a step that its key already canceled.

A compensation for an unknown step must also handle "maybe nothing ran". The service that holds the effect answers "not found" for a step that never applied. Treat that answer as success:

```go
func (h *hotel) Compensate(ctx context.Context, b *booking) error {
	err := h.hotels.Cancel(ctx, sagas.IdempotencyKey(ctx))
	if errors.Is(err, errNotFound) {
		return nil
	}

	return err
}
```

If you leave the three cases untested, a charge that lands late is never refunded. The `sagastest` helpers below inject each case.

## The state is a closed set of plain types

Every action and compensation reads and writes one state value of type `S`. The library copies it for each attempt, commits the changes of a successful attempt in one step and drops the copy of a failed attempt. To do this without a method from you, the library accepts only plain data.

An allowed state is a struct. Its fields can be:

- `bool`, integers, `float32`, `float64` and `string`
- structs with exported fields, with no embedded field and no tag
- arrays and slices
- maps with `string` or integer keys
- pointers that do not point at a pointer, a slice or a map

No type defines its own encoding. That rules out `MarshalJSON`, `MarshalText` and the related methods. It also rules out `time.Duration` and `jsontext.Value`. Store a time as an `int64` of Unix nanoseconds or as an RFC 3339 `string`. Store a duration as an `int64` of nanoseconds.

```go
type booking struct {
	ID        string
	Guests    []string
	Amount    int64
	PaymentID string
	PaidAt    int64
	Notes     map[string]string
	Referral  *string
}
```

`New` rejects a type that breaks a rule, and the error names the field path and the rule. A list of rules is simpler than a proof of a round trip. It also finds the problem at build time, not at the first run.

Three more facts follow from this design:

- Two pointers to one object become two objects in the copy.
- A value that cannot be encoded fails the commit with `ErrInvalidState`. Examples are NaN, positive or negative infinity, a pointer cycle and a `string` with invalid UTF-8.
- Two parallel steps that change the same top-level field cause a conflict. The commit of the later step fails with `ErrConflict`, and that failure is permanent. A map or slice field counts as one field. Two steps that touch different keys of one map still conflict. Two steps that write different fields never conflict.

A step sees the state as of its start, with every commit of its ancestors. Write skew between sibling steps is possible: each reads the old value of a field that the other writes.

## The run contract

These rules describe how a run ends. They matter most for a caller cancel or a journal fault.

### The grace period

While the caller's context is alive, the saga runs, bounded by the attempts and by the `AttemptTimeout` of each step. When the caller's context ends, the saga starts nothing new on the caller's behalf. Recovery then gets at most `RecoveryTimeout` from that moment. Recovery means the compensations, or the pivot and the forward-only zone. The default is five minutes.

If the caller's context never ends, no extra clock runs. A caller that wants a bound on the whole run sets a deadline on its own context. A hung compensation waits until that context ends, and then the grace period starts.

When the grace period ends, backward recovery ends `ErrCompensationFailed` and forward recovery ends `ErrIncomplete`. The library marks the end with `ErrGraceEnded`.

A rule follows for the step author: every step and every compensation honors its context and is idempotent. A call that returns after the grace end can miss the journal. It still shows in the error of `Run`.

### Why `AttemptTimeout` covers only the action

`AttemptTimeout` bounds each attempt of the action. It does not bound a compensation attempt. Only `RecoveryTimeout` bounds those. A hung compensation attempt can use the whole recovery time. If you want a cut on each compensation attempt, write middleware that reads the phase from `info.Phase`.

### Reading the error of `Run`

If every step finished, `Run` returns nil. For any other end that began, it returns a `*sagas.RunError`. Its `Kind` is `ErrCompensated`, `ErrCompensationFailed`, `ErrIncomplete`, `ErrConcurrentWriter` or one of the journal kinds. `Steps` lists every failed step in order. `Pending` lists the steps that still need action. `errors.Is` finds the kind and every cause inside.

```go
err := saga.Run(ctx, &b)

var runErr *sagas.RunError
if errors.As(err, &runErr) {
	fmt.Println(runErr.Kind, runErr.Pending)
}
```

Five more rules apply to this error.

1. A cancel can look like a step failure. A step's own error can carry `context.Canceled`, so a match on that error does not prove that a cancel stopped the run. To learn whether the caller canceled, test your own `ctx.Err()` or `errors.Is(err, sagas.ErrGraceEnded)`.
2. `context.Canceled` is one value. A journal that cancels its own work internally gives the same value as the grace end. The library cannot tell them apart, so it keeps the whole journal error and joins `ErrGraceEnded` to it.
3. A late cancel does not undo finished work. If every step succeeded before the cancel mattered, the run ends completed and returns nil. A canceled context still stops every launch that did not start, and then the run compensates.
4. After `ErrConcurrentWriter`, `Pending` informs you and is not a to-do list. Another writer owns the execution. The library stops at once without compensation, because the writer that now owns the execution decides what to do. It writes the state that it committed so far back into your state value.
5. The journal is the durable source. `Pending` is the conservative view of the run. After a journal fault, `Pending` can owe more than the journal, and never less. Repeating a step that the journal shows as done is safe because the call is idempotent. One case departs from this. A step that did not run can get a closing append that also fails. Then the journal lists the step as unknown, and a compensation runs on nothing. The call must handle that case, as the second compensation case above says.

### When a compensation fails

Set `CompensationFailure` on a step to choose the policy. `CompensationBlock` is the default. It stops every later compensation that depends on the failed one, across steps that have no compensation, and lets independent branches go on. `CompensationHalt` stops every remaining compensation. `CompensationContinue` stops nothing.

`CompensationContinue` departs from the 1987 paper, which stops the recovery and hands it to a person. It exists for systems where undoing the rest is worth more than stopping, and it can leave a step half done. Each policy ends the run `ErrCompensationFailed`. `CompensationBlock` and `CompensationHalt` hand the execution to a person for manual repair.

## The journal

The journal is an append-only record. The library writes the intent of each action and each compensation before it runs the call. Nothing happens outside the journal. If the intent cannot be written, the action does not run. If the outcome cannot be written, the step counts as unknown and the library compensates it. If the journal stays down during recovery, the compensations do not run, and the run ends `ErrCompensationFailed` with `ErrJournal`. The other choice, to go on in memory, leaves silent gaps in the record.

Each event carries a sequence number and a writer token. If its sequence number does not follow the last one, the journal refuses the event. An exact resend by the same writer succeeds and writes nothing. Any other event at a taken number is a rejection, and the rejected run stops. This rule keeps one writer for each execution. It is a condition on the write, and it is not an acknowledgment.

## The `sagastest` contract

The `sagastest` package turns the rules above into tests that you run on your own saga. Each helper exists for one rule.

A test gives each helper a `Setup`. The `Setup` returns a fresh `Case` each time the helper calls it. The `Case` holds the `Saga`, its `State`, a `Check` of your fakes, the `Flow` that you gave to `New`, and `Late`. The `Setup` passes the middleware that the helper gives it to `sagas.WithMiddleware`. If it drops that middleware, the helper cannot inject a fault, and it fails the test with a message that names this cause. A flow whose steps are all disabled gives the same symptom, and the message names that cause too.

- `Compensations` runs three cases for each step that has a compensation. They match the three compensation cases above: never applied, run twice after the action landed, and the step landing after its compensation. `Late` sends the action again to your fakes with the same idempotency key. `Late` and the compensation commute, so the order does not change the result. An error from `Late` goes to the test log only, and `Check` decides the case. The twice case starts from an action that landed. A start from a step that never applied lets a compensation pass although it is not idempotent over a landed effect.
- `Pivot` injects an unknown outcome on the first attempt of the pivot. With two or more attempts, the run completes and the final state equals the state of a clean run. With one attempt, the run ends `ErrIncomplete`, only the pivot is pending and no compensation runs. This is a valid design that ends in manual resolution, so the helper does not fail it. The helper compares the final state with `reflect.DeepEqual`.
- If the flow has no step to test, a helper fails the test. If a step that it exercises does not start in its own case, the helper fails the test too. A silent skip leaves a step untested with no warning.
- `Journal` runs the contract that every journal must meet. The rows cover these cases: the sequence, a gap, a number below 1 and a resend. They also cover a different event at a taken number, the two rejection errors and the stored copy. The last rows cover a done context, invalid events and an equal instant in another time zone. They also cover the exact resend and the race for the numbers 1 to 3. The `newJournal` function must return a fresh, empty journal on each call. The package offers no journal that stores an event and then fails. Such a fault is ambiguous, and the core already proves its handling.
- `Fail`, `Unknown` and `Panic` inject a fault in one step as middleware. `Fail` panics on a nil error. The `Recorder` records every call, and a call that panics gets an `Err` that wraps `sagas.ErrPanic`.

## Where to go next

The glossary defines each term that this page uses. The page on design decisions gives the reason behind each choice in the API.
