# The guarantees of a saga

This page explains what the library promises, what it leaves to you, and why. It describes the contract. The how-to guides carry the steps. The [glossary](../glossary.md) defines the core terms. This page explains each other term that it needs where the term first appears. The [design decisions](design-decisions.md) page gives the reason behind each choice in the API.

The ideas come from four sources:

- [Garcia-Molina and Salem, "Sagas" (1987)](https://www.cs.cornell.edu/andru/cs711/2002fa/reading/sagas.pdf) introduce the saga and its compensations.
- [McCaffrey, Kingsbury and Narula, "Distributed sagas" (2016)](https://github.com/aphyr/dist-sagas/blob/master/sagas.pdf) treat the unknown outcome of a call as a normal case.
- [Sookocheff, "Fault tolerance and data consistency using distributed sagas" (2018)](https://sookocheff.com/post/architecture/fault-tolerance-and-data-consistency-using-distributed-sagas/) describes the pattern for services.
- [Mehrotra, Rastogi, Silberschatz and Korth, "A transaction model for multidatabase systems" (1992)](https://doi.org/10.1109/ICDCS.1992.235055) gives the terms compensatable, retriable and pivot. This library maps them to the compensable zone, the forward-only zone and the pivot.

## What is all-or-nothing

A saga is not a database transaction. No lock holds the steps together, and other readers see the effect of each step as soon as it ends. The promise is smaller. When the saga ends, either every step is done or the library has undone the part that was done.

The library keeps this promise in three ways.

1. It compensates every started step of the compensable zone that has a compensation. A step that failed, timed out or was canceled can still change something outside the saga. A charge that the bank confirms after your timeout is the usual example. A library that compensates only completed steps never refunds that charge.
2. It stops the running steps before it compensates. On the first failure, it cancels the context of the running steps and starts nothing new. It waits for every step to return, and only then starts the compensations. A branch that compensates while a sibling (a step that runs in parallel) still writes the state mixes the two phases in the journal. The journal is the append-only record of the run, which a later section describes. `Run` never returns while a goroutine that it started is alive.
3. It compensates along the reversed dependency graph. `sagas.Sequence(items...)` and `sagas.Parallel(items...)` take steps or groups and return a `Flow`, the value that `New` takes as its second argument. One step is also a `Flow`. Together they build a graph of dependencies: in a `Sequence`, a step depends on the steps before it. "Reversed" means that a compensation waits for the compensations of every started step that depended on its step. Steps that do not depend on each other compensate in parallel. Only the graph decides the order, forward and backward. A business order between two steps is a dependency in the `Flow`.

A step can have an optional method `Enabled(*S) bool`. If it returns false, the library skips the step. A skipped step never started, so the library does not compensate it, and it counts as done for the steps that follow.

## The pivot

Some steps cannot be undone. A payment that left the bank, an email that went out, a parcel that shipped: a compensation for these steps is only a new fact, and sometimes there is none. The pivot is the step where the saga passes the point of no return. Its call is atomic, which means that it applies whole or not at all.

A pivot splits the saga into two zones. If a saga has no pivot, every step is in the compensable zone.

- The compensable zone holds the steps before the pivot. A failure here undoes them.
- The forward-only zone holds the steps after the pivot. A failure here never undoes anything.

`New` rejects a saga that breaks one of these rules. A saga has at most one pivot. Every other step comes before the pivot or after it in the `Flow`. The pivot has no `Compensate` and no `Enabled`. No step after the pivot has `Compensate`. The error comes from `New`, not from a surprise at run time.

At run time, a failure of the pivot call means that it did not apply, so the library undoes the compensable zone. The run then ends `ErrCompensated`: `Run` returns a `*RunError` whose `Kind` is `ErrCompensated`, as the section on the error of `Run` describes.

An unknown outcome from the pivot is different. An unknown outcome follows a timeout, a cancel or a panic of the call. Nobody knows then whether the effect happened. The library retries the pivot with the same idempotency key (defined below) and never undoes it. When the attempts run out, the run ends `ErrIncomplete` with only the pivot pending, and the library compensates nothing. "Pending" means that the step still needs action from a person or a new run. The section on the error of `Run` below says more. A failure after the pivot also ends `ErrIncomplete`, because the saga must finish and cannot go back.

The pivot call runs in one retry loop on a context that the caller's cancel does not stop. The reason is that a cancel half way through a pivot leaves its outcome unknown. The grace period below bounds that loop.

The committed state is the state value that the successful attempts so far changed. A commit applies the changes of one successful attempt to it. If the library cannot encode the new state, the commit fails, as the section on the state explains. A conflict between parallel steps cannot fail the commit of the pivot, because the pivot has no parallel sibling. If the pivot call succeeds and its commit fails, the library counts the pivot as unknown. It commits nothing and compensates nothing. The run ends `ErrIncomplete` with the pivot in `Pending` and `ErrInvalidState` in its error. The other choice, to count the pivot as failed, compensates around a pivot that did apply.

## At-least-once and idempotency

The library calls each action and each compensation at least once. It can call one twice. A retry after a timeout, a resend after a journal fault and a compensation after an unknown outcome all repeat a call. The journal section below explains the resend. The library cannot know whether the first call changed anything outside, so it asks you for one thing in return: every action and every compensation must be idempotent, which the next paragraph defines.

A call that runs twice has the same effect as a call that runs once. This is idempotency. The library helps with a key. In a step method, `sagas.IdempotencyKey(ctx)` returns the execution ID, a slash and the step name. The key is the same on every attempt and on the compensation. A step that passes the key to the outside service lets the service recognize a repeat:

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

Middleware is a value with a `Wrap(ctx, info, call)` method. Its `info` argument is a `sagas.CallInfo`, and the same key is `info.IdempotencyKey()`. The key holds the execution ID. If the ID changes between requests, the key changes too. A state with an `ExecutionID() string` method sets the ID, and a business value such as an order number works best. Without that method, the library draws a random ID, and a repeated request looks like a new execution.

### Retries

A step retries its action and its compensation under one policy. The default is three attempts with an exponential delay and jitter. `Retry` on the step changes it. Jitter is a random change to each delay. A step with `sagas.NoRetry()` makes one attempt, and its compensation also gets one attempt. A caller that wants a more persistent compensation raises `Retry.Attempts`. `RecoveryTimeout` bounds the whole recovery, and `sagas.WithRecoveryTimeout(d)`, passed to `New`, sets it. Recovery is the work after a failure or a caller cancel. Backward recovery runs the compensations. Forward recovery finishes the pivot and the forward-only zone. The grace period below defines the bound.

The retries stop at the first of these events. The attempts run out. The call returns an error that carries the mark of `sagas.Permanent(err)`. The call panics. The deadline passes. Before the pivot, the deadline is the caller's context. During recovery, it is `RecoveryTimeout`. The `Permanent` mark counts at any depth of the error chain, so wrapping it with `%w` keeps it. A panic is a code defect, and a retry only repeats it.

The library does not limit how many steps run at once or how fast. That is load control, and it varies with each application. Middleware carries it.

## The three compensation cases

A compensation must be correct in three cases. The library treats an unknown outcome as normal, so each case can happen in production.

1. The action never applied. The call timed out, was canceled or failed before it changed anything, and the compensation still runs. It does nothing and returns success.
2. The compensation runs twice. A retry or a resend repeats it. The second run has the same effect as the first.
3. The action lands late, after its compensation. A slow call that the library gave up on can still reach the outside service later. The compensation tolerates this, usually because the service refuses an action whose key it already canceled.

A compensation for an unknown step must handle "maybe nothing ran". The service that holds the effect answers "not found" for a step that never applied, and the compensation returns success for that answer:

```go
func (h *hotel) Compensate(ctx context.Context, b *booking) error {
	err := h.hotels.Cancel(ctx, sagas.IdempotencyKey(ctx))
	if errors.Is(err, errNotFound) {
		return nil
	}

	return err
}
```

If the three cases stay untested, a charge that lands late is never refunded. The `sagastest` helpers below inject each case.

## The state is a closed set of plain types

Every action and compensation reads and writes one state value of type `S`. The library copies it for each attempt, commits the changes of a successful attempt at once and drops the copy of a failed attempt. To do this without a method from you, the library copies and reads the state through its JSON encoding, the `encoding/json/v2` package of Go 1.27. The state must therefore be plain data that this encoding handles without help.

A state is a struct. Its fields can be:

- `bool`, integers other than `uintptr`, `float32`, `float64` and `string`
- structs with exported fields, with no embedded field and no tag
- arrays and slices
- maps with `string` or integer keys
- pointers that do not point at a pointer, a slice or a map

No type defines its own encoding. That rules out `MarshalJSON`, `MarshalText` and the related methods. It also rules out `time.Duration` and `jsontext.Value`, a type of the `encoding/json/jsontext` package. A time is an `int64` of Unix nanoseconds or an RFC 3339 `string`. A duration is an `int64` of nanoseconds.

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

`New` rejects a type that breaks a rule, and the error names the field path and the rule. A list of rules is simpler than a test that encodes a sample value and decodes it again to see that nothing changed. The list also finds the problem in `New`, before the first run.

Three more facts follow from this design:

- Two pointers to one object become two objects in the copy.
- A value that the encoding cannot handle fails the commit with `ErrInvalidState`. Examples are NaN, positive or negative infinity, a pointer cycle and a `string` with invalid UTF-8.
- Two parallel steps that change the same top-level field cause a conflict. The commit of the later step fails with `ErrConflict`, and that failure is permanent. A map or slice field counts as one field, so two steps that touch different keys of one map still conflict. Two steps that write different fields never conflict.

A step sees the state as of its start, with every commit of the steps that it depends on. Write skew between sibling steps is possible. Write skew means that each step reads the old value of a field that the other step writes.

## The run contract

These rules describe how a run ends. They matter most for a caller cancel or a journal fault.

### The grace period

While the caller's context is alive, the saga runs, bounded by the attempts and by the `AttemptTimeout` of each step. When the caller's context ends, the library starts no new step of the compensable zone and no new pivot. The forward-only zone goes on until the grace period ends. Recovery then gets at most `RecoveryTimeout` from that moment. This time is the grace period. The default is five minutes.

If the caller's context never ends, no extra clock runs. A caller that wants a bound on the whole run sets a deadline on its own context. A hung compensation waits until that context ends, and then the grace period starts.

When the grace period ends, backward recovery ends `ErrCompensationFailed` and forward recovery ends `ErrIncomplete`. The library marks the end with `ErrGraceEnded`.

A rule follows for the step author. Every step and every compensation honors its context and is idempotent. A call that returns after the grace end can miss the journal. It still shows in the error of `Run`.

### Why `AttemptTimeout` covers only the action

`AttemptTimeout` bounds each attempt of the action. It does not bound a compensation attempt. Only `RecoveryTimeout` bounds those. A hung compensation attempt can use the whole recovery time. Middleware can cut each compensation attempt, because it reads the phase from `info.Phase`. The phase is `PhaseExecute`, `PhasePivot` or `PhaseCompensate`.

### Reading the error of `Run`

If every step finished and the journal recorded the end, `Run` returns nil. An error before the saga-started event is stored returns as it is. Examples are `ErrInvalidRun`, `ErrInvalidState`, `ErrExecutionExists` and an `ErrJournal` error. For any other end that began, `Run` returns a `*sagas.RunError`. Its `Kind` is `ErrCompensated`, `ErrCompensationFailed`, `ErrIncomplete`, `ErrConcurrentWriter`, `ErrJournal` or `ErrGraceEnded`. A writer is one `Run` call. A second `Run` with the same execution ID is another writer, for example in another process. `ErrConcurrentWriter` means that another writer appended to the execution first. `ErrJournal` and `ErrGraceEnded` mark a run whose steps all succeeded but whose journal write failed. `Steps` lists every failed step in order. `Pending` lists the steps that still need action. `errors.Is` finds the kind and every cause inside.

```go
err := saga.Run(ctx, &b)

var runErr *sagas.RunError
if errors.As(err, &runErr) {
	fmt.Println(runErr.Kind, runErr.Pending)
}
```

Five more rules apply to this error.

1. A cancel can look like a step failure. A step's own error can carry `context.Canceled`, so a match on that error does not prove that a cancel stopped the run. To learn whether the caller canceled, a caller tests its own `ctx.Err()` or `errors.Is(err, sagas.ErrGraceEnded)`.
2. `context.Canceled` is one value. A journal that cancels its own work internally gives the same value as the grace end, so the library cannot tell them apart. If a journal error mixes the context error with other errors, the library keeps the whole journal error and joins `ErrGraceEnded` to it. If every part of the error is the context error, the error carries only `ErrGraceEnded`.
3. A late cancel does not undo finished work. A cancel that arrives after the last step started does not compensate. If every step then succeeds, the run ends completed and returns nil. Before the pivot, a canceled context still stops every step that did not start yet, and then the run compensates. The forward-only zone does not stop for a cancel until the grace period ends.
4. After `ErrConcurrentWriter`, `Pending` informs you and is not a to-do list. Another writer owns the execution. The library starts nothing new, waits for the running steps and compensates nothing, because the writer that owns the execution decides what to do. It writes the state that it committed so far back into your state value.
5. The journal is the durable source. `Pending` is the conservative view of the run. After a journal fault, `Pending` can owe more than the journal, and never less. Repeating a step that the journal shows as done is safe, because the call is idempotent. One case departs from this. A step that did not run can get a closing append that also fails. The journal section below explains the closing append. Then the journal lists the step as unknown, and a compensation runs on nothing. The compensation handles that case, as the second compensation case above says.

### When a compensation fails

`CompensationFailure` on a step sets the policy. `CompensationBlock` is the default. It stops every later compensation along the reversed graph, across steps that have no compensation, and lets independent branches go on. For example, steps A, B and C run in a chain, and B has no compensation. If the compensation of C fails, the library still blocks the compensation of A. `CompensationHalt` stops every remaining compensation. `CompensationContinue` stops nothing.

`CompensationContinue` departs from the classic model. The 1987 paper hands an erroneous transaction to a programmer for manual repair, and it does not go on past it. `CompensationContinue` exists for systems where undoing the rest is worth more than stopping. It can leave an earlier step undone while a later step stays done. Each policy ends the run `ErrCompensationFailed`. `CompensationBlock` and `CompensationHalt` hand the execution to a person for manual repair.

## The journal

The journal is an append-only record of what an execution intends and did. Each record is an event. The library writes the intent of each action and each compensation before it runs the call. The library takes no action that the journal has not recorded first. If the intent cannot be written, the action does not run. If the outcome cannot be written before the pivot, the step counts as unknown and the library compensates it. A pivot error whose failure record cannot be written still counts as failed. If the journal stays down during recovery, the compensations do not run, and the run ends `ErrCompensationFailed` with `ErrJournal`. The other choice, to go on in memory, leaves silent gaps in the record.

Each event carries a sequence number and a writer token. The writer token is a random value that each `Run` makes once. If an event has a sequence number that does not follow the last one, the journal refuses it. An exact resend by the same writer succeeds and writes nothing. Any other event at a taken number is a rejection, and the rejected run stops. This rule keeps one writer for each execution. It is a condition on the write, and it is not an acknowledgment.

After a failed append, the library does not know whether the event landed. Before its next append, it sends the exact same event again. This is the resend. A resent intent can land for a step that never ran. The library then appends a closing failure for that step. This way the record does not show a run that did not happen.

## The `sagastest` contract

The `sagastest` package turns the rules above into tests that you run on your own saga. Each helper exists for one rule.

A test gives each helper a `Setup`, a function that returns a `Case`. The helper calls `Setup` once for each case that it runs, so each case gets a fresh saga and fresh fakes. Fakes are test doubles of the outside services, such as a hotel service in memory. The `Case` holds these fields:

- `Saga` is the saga under test.
- `State` is its state.
- `Check` compares the fakes with what the saga must leave behind. The helper runs it after each case. Each `Compensations` case ends with a compensated run, and each `Pivot` case ends completed or `ErrIncomplete`, so `Check` accepts both.
- `Flow` is the `Flow` that the `Setup` gave to `New`. The helpers read the steps from it.
- `Late` sends the action of a step again to the fakes, as a late call, with the idempotency key that the helper passes.

The `Setup` passes the middleware that the helper gives it to `sagas.WithMiddleware`. If it drops that middleware, the helper cannot inject a fault, and a clean run records no call. A flow whose steps are all disabled gives the same symptom. In both cases the helper fails the test once, and the message names both causes.

- `Compensations` runs three cases for each step that has a compensation. They match the three compensation cases above: never applied, run twice after the action landed, and the action landing late after its compensation. In the third case, the helper runs the saga to its end, calls `Late`, and then runs `Check`. The helper runs the compensation first and the late action after it, and `Check` must accept the result. An error from `Late` goes to the test log only, and `Check` decides the case. The twice case starts from an action that landed. A start from a step that never applied lets a compensation pass although it is not idempotent over a landed effect.
- `Pivot` injects an unknown outcome on the first attempt of the pivot. With two or more attempts, the run completes and the final state equals the state of a clean run. With one attempt, the run ends `ErrIncomplete`, only the pivot is pending and no compensation runs. This is a valid design that ends in manual resolution, so the helper does not fail it. `Pivot` also repeats each forward-only step to test its idempotency. It compares the final state with `reflect.DeepEqual`.
- If the flow has no step to test, a helper fails the test. If a step that it exercises does not start in its own case, the helper fails the test too. A silent skip leaves a step untested with no warning.
- `Journal` runs the contract that every journal must meet. The rows cover these cases: the sequence, a gap, a number below 1 and a resend. They also cover a different event at a taken number and the two rejection errors, `ErrExecutionExists` and `ErrConcurrentWriter`. Another row covers the stored copy, which the journal keeps apart from the caller's event. The last rows cover a context that is already done, invalid events and an equal instant in another time zone. They also cover the exact resend and two writers that race for the numbers 1 to 3. The `newJournal` function must return a fresh, empty journal on each call. The package offers no journal that stores an event and then fails. Such a fault is ambiguous, because the caller cannot tell whether the journal stored the event. The core already proves its handling.
- `Fail`, `Unknown` and `Panic` inject a fault in one step as middleware. `Fail` returns the error without running the call, and it panics on a nil error. `Unknown` runs the call, then returns `context.DeadlineExceeded` as a lost reply. `Panic` runs the call, then panics, so the effect applies and the outcome is unknown. The `Recorder` records every call, so a test can assert on the calls that the saga made. `Err` is the error field of each recorded `Call`. A call that panics gets an `Err` that wraps `sagas.ErrPanic`.
