# Design decisions

This page explains why the library looks the way it does. Each entry gives the decision, the reason, and the main alternative that the design rejects. The page groups the entries by topic. For the promises that follow from these choices, see the page on guarantees.

## The shape of the library

### A breaking rewrite

The library replaces the whole old API and ships as `v0.1.0`. The module path stays the same. The old API hid compensation, which is the central idea of the pattern, so a fix of its internals was not enough. Current users must migrate. The old irregular tags stay in place, because deleting a published tag breaks users who depend on it.

### A definition apart from its runs

`sagas.New` builds and checks an immutable definition and returns `(*Saga[S], error)`. `Run` starts one execution of it. Many executions can run at the same time. The rejected design was one mutable saga object that you configure and run in place. That object cannot run twice at once. Every problem in a definition comes back as an error from `New`, and `New` never panics.

### One typed state

A saga is generic over one state type `S`. Every action and compensation takes `*S`. The alternative was a typed output for each step. In a graph, the outputs have different types, so the design ends in `any` and type assertions. A `map[string]any` is worse, because its errors appear only at run time.

### The standard library only

The core module imports only the Go standard library. The repository ships the ports that the core owns, such as the journal, and no adapter for a vendor. An adapter for a transport, a store or a telemetry system lives outside this repository. The two rejected options were vendor modules in this repository and integrations inside the core. The first carries vendor code and its upkeep. The second makes every user download every client library. For the same reason, the library uses `errors` from the standard library and `log/slog`, and not a third-party error or log package. Tests use `testify`, which is a test-only dependency.

### Go 1.27

The module requires Go 1.27 and CI runs on it. Users on Go 1.26 cannot import the module. The state rules also follow the `encoding/json/v2` package of that release.

### One way to do each thing

Each task has one way in the public API. Options that configure a saga carry the `With` prefix, such as `WithJournal`, `WithMiddleware` and `WithRecoveryTimeout`. The steps declare the content of a saga, so they carry no prefix. Two rules were rejected. "Never use `With`" makes option names collide with the `Journal` and `Middleware` types. "Use `With` only on a collision" makes each new option guess. A new feature that adds a second way to do a task must remove the first way.

### Defaults you can read

Every configurable value has an exported constant for its default, such as `DefaultAttempts` and `DefaultRecoveryTimeout`. A zero value means that default, so `RetryPolicy{}` is the default policy. Fields have names that make this work, such as `NoJitter`. A default variable is the rejected option, because any package can change it for the whole program.

### Option types are function types

`Option` is a function type with an unexported parameter. Callers write the same code as with an interface. The interface form needed a lint exception for a pattern that the package uses nowhere else. A nil option, or an option given twice, is an error from `New`. The reason is that "last one wins" makes the first line lie to the reader.

## Steps and the flow

### A step is a type that embeds `sagas.Step`

A step is a pointer to your own type. The type embeds `sagas.Step`, which holds `Name`, `Description`, `Retry`, `AttemptTimeout` and `CompensationFailure`. The type has `Execute` or `Pivot`, and can have `Compensate`, `Enabled` and `Wrap`. What a step can do is then visible in its methods. The rejected design used functional options on a step. Closures spread over the options, and each option needed its own repeat check. A step with an empty `Name` takes its name from the type, so `CheckFraud` becomes `check-fraud`. A saga with a durable journal sets `Name`, so a rename of the type keeps the stored names.

### `Sequence` and `Parallel` order the steps

`Sequence` runs its items one after another. `Parallel` runs its items together. One pointer is one step, so a step that appears in two places runs once, with the dependencies of both places. Any graph without a cycle can be written this way. The rejected design was an `After` list of step names. It repeats names across steps. A typo shows only at the call of `New`.

### Names use a short ASCII set

A saga name, a step name and an execution ID hold only ASCII letters, digits, `-`, `_` and `.`. They start with a letter or a digit. Log and metric systems change other characters, and `Mermaid` writes each name as it is. Allowing every Unicode letter was rejected for that reason.

### A saga needs at least one step, and nothing nil

`New` rejects a saga with no steps, because the run reports a success that did nothing. It also rejects a nil function, a nil middleware and a nil journal. A nil function panics at call time and becomes an unknown outcome. A typed nil inside a journal value counts as nil too.

### `Inspect` reports a flow the way `New` does

`Inspect` runs the same checks as `New` and returns one report for each step. The report holds the name, the settings, one flag for each step method and the resolved dependencies. `New` calls `Inspect`, so both return the same errors. A step type that gets one method from two embedded fields is rejected, at every depth, as Go selector rules treat it. Without that rule, a method silently disappears from the step.

### A conditional step is skipped, not started

`Enabled` runs once, after the dependencies of the step finish. The library records the result and never asks again. A skipped step never started, so it is never compensated, and the steps after it still run. The rejected options were a special `ErrSkip` error and a plain `return nil`. The first starts the step. The second compensates a step for nothing and reports a false success. A worker evaluates the predicate on the step goroutine, so a slow predicate cannot stall the other steps.

## Compensation

### Every started step gets its compensation

A step that succeeded, failed, timed out or was canceled gets a compensation. A skipped step does not. A call that timed out can still land outside later. A library that compensates only completed steps never refunds a charge that the bank accepts late.

### Compensation follows the reversed graph

A compensation starts after the compensations of the started steps that depended on it. Independent branches compensate in parallel. A fixed order for independent steps is slower and arbitrary. A business order belongs in the flow, so no sequential-compensation option exists.

### Stop the siblings, then compensate

On the first failure, the library cancels the running steps, starts nothing new and waits for every step goroutine. Only then do compensations start. Letting siblings finish creates real effects that the library then undoes. Compensating each branch as it resolves runs compensations while forward steps still write the state, and the journal mixes the two phases. A step that ignores its context blocks `Run`, and the contract says so.

### The failure policy is set on the step

`CompensationFailure` on a step picks the policy for a failed compensation. `CompensationBlock` is the default. It stops every later compensation along the reversed graph, across steps that have no compensation, and lets independent branches go on. `CompensationHalt` stops all remaining compensations. `CompensationContinue` stops nothing. There is no saga-wide default, because the user must choose for each step. A policy on a step without `Compensate` is an error. `CompensationContinue` is an explicit departure from the 1987 model, kept for systems where undoing the rest is worth more than stopping.

### Each compensation is decided at its turn

The library decides a compensation after every compensation that it waits on ends. An earlier halt wins, then a block, and otherwise the compensation runs. A mark at the moment of the failure makes the events of a step after two failures depend on timing.

## The pivot

### Pivot rules are checked when you build the saga

`New` enforces at most one pivot. Every other step must come before or after it. The pivot has no compensation and no condition. No step after the pivot has a compensation. Real sagas often have a point of no return, and no Go library enforced these rules. The pivot must be atomic, and the forward-only steps must be idempotent. Both facts are in the contract, and `sagastest` exercises them.

### One kind of step

The library has no separate "retriable" step kind. The pivot is a step with `Pivot`, and the forward-only zone carries its rules. A retriable kind adds a term for a position that the graph already shows.

### The pivot retries on a context that a cancel does not stop

The whole pivot call runs in one retry loop that the caller's cancel does not stop. Two loops, one on the caller's context and one detached, break the single retry loop and need a rule to count attempts across them. A pivot that panics is not retried. A panic is a code defect, and a retry repeats it.

### A pivot error that the journal cannot record

A pivot that returned an error counts as failed. The journal can fail to record the error, and the rule stays. The coordinator knows that it did not apply. It then compensates the compensable zone. Keeping the pivot unknown stops the run half way for no reason. A pivot that succeeded but whose commit failed counts as unknown, because it did apply and the saga cannot undo it.

## Retries and time

### One retry policy on the step

`Retry` on the step governs the action and the compensation. A saga-wide retry option is a second way to set the same thing. The policy is a struct with `Attempts`, `Initial`, `Max`, `Multiplier` and `NoJitter`. The defaults are three attempts, 200 ms initial delay, 30 s maximum delay, a multiplier of 2 and jitter. `NoRetry()` is a function that returns one attempt, so no caller can change a shared value.

### One stop rule

A retry loop stops at the first of four events: the attempts run out, an error carries the `Permanent` mark, the call panics, or the deadline passes. The mark counts at any depth of the error chain. Counting only the top level breaks a caller who wraps the mark with `%w`.

### The attempt timeout covers the action only

`AttemptTimeout` bounds each attempt of the action. A compensation attempt has no timeout of its own. A shared option for both phases brings back a per-attempt compensation timeout through the action's option. To cut each compensation attempt, write middleware that reads the phase.

### One grace period for recovery

While the caller's context lives, the saga runs. After the caller gives up, recovery gets `RecoveryTimeout` from that moment, five minutes by default. The rejected designs started a clock at each phase, which sums the phases, or from the first failure, which still depends on the phase. Stopping the forward-only zone on a cancel was also rejected, because it creates manual work.

### The grace end has a name

A grace timer ends the recovery context with `ErrGraceEnded`, so a caller can test for it with `errors.Is`. A plain cancel hides why recovery stopped. The library joins `ErrGraceEnded` to the error of each compensation and each journal append that the grace end stopped. A compensation that the grace end alone fails has the kind `ErrGraceEnded`, because `ErrFailed` says that the caller's code failed, and it did not. A late success is still recorded: a late compensation counts as compensated, and a late forward success is committed. The library launches nothing new after the grace end.

### What matches the caller's cause

The error of `Run` matches the cause of the caller's context only where the cancel interrupted work. If the two differ, it matches both `ctx.Err()` and the cause. Matching after every cancel makes a real forward-only failure look like a cancel.

### A late cancel does not undo finished work

If every step succeeded before a late cancel mattered, the run completes. Compensating after the barrier undoes a saga that already reached its goal.

## State and commits

### The state follows plain rules

The state is a closed set of plain types, and `New` names the field path and the rule of any state that breaks them. The rule list follows the JSON encoding of Go 1.27. Rejecting every standard library type needs build information and refuses types that round-trip, such as `time.Month`. A proof of a round trip in `New` needs a generator for every hard case. Its error names a difference, not a rule.

### Attempts work on a private copy

Each attempt works on a private copy of the committed state. A successful attempt commits its changes in one step, and a failed attempt drops the copy. The commit is a three-way merge of top-level fields. Two concurrent changes to one field fail the later commit with `ErrConflict`. A documented contract of "disjoint fields" with only the race detector lets a mistake corrupt the state silently. A lock for each step serializes the saga.

### A rejected run writes its state back

After a mid-run rejection by another writer, `Run` writes the state committed so far into your state value. Leaving it as it was hides an effect that really happened. If nothing ran, your state keeps its value.

## The journal

### One small port

`Journal` has one method, `Append(ctx, *Event)`. The library writes the intent before each action and compensation, and commits are events. A journal stores its own copy and never keeps the caller's pointer. The journal is not a queue. It never delivers, removes or marks events. Messaging belongs to a separate port.

### The journal fails closed

If the intent cannot be written, the action does not run. If the outcome cannot be written, the step is unknown and gets compensated. A journal that stays down ends a run `ErrCompensationFailed` with `ErrJournal`. Going on in memory leaves silent gaps in the record. The library does not retry appends, because a journal implementation owns its own retries.

### A single writer

Each event has a sequence number and a writer token. The journal appends only the next number. An exact resend by the same writer succeeds, so a lost reply does not stop a healthy run. Any other event at a taken number is a rejection. A second writer on a running execution stops the run without compensation, because that writer owns the execution now. A claim that the journal assigns numbers adds two concepts and a larger contract.

### Events build the result

The saga-started event carries every step name and its zone. Compensations that were blocked or halted have their own events. The result builder reads only events, so a reader with only the journal can rebuild the outcome. The library also resends the exact event of a failed append before it writes anything new. This repairs the case where the first write landed and its reply was lost.

### The run error can owe more than the journal

After an unclear append, `Pending` can list more than the journal shows, and never less. The journal never holds two outcomes for one step. Undoing each decision to match a landed event reverses decisions that the library already passed to other steps.

### Appends honor their context

Every append gets the grace context and honors it. A stuck journal then cannot hold `Run` past the grace period. The last events can be missing in that case, and the error matches `ErrJournal`.

## Middleware and errors

### The core is a fixed pipeline

Each step gets a pipeline of private middleware in one order: retry, call info, timeout, recovery, your middleware, then the call. One function builds it. The core does not export these pieces, because the guarantees must not depend on a user who installs them. Your middleware wraps every attempt and counts in the attempt timeout.

### Middleware is a value with `Wrap`

`Middleware` is an interface with `Wrap(ctx, info, call)`. The call data arrives as the `CallInfo` parameter, and middleware never receives the state. A function with the same signature adapts through `MiddlewareFunc`. A step method reads the idempotency key with `IdempotencyKey(ctx)`, and middleware reads `info.IdempotencyKey()`. `Chain` joins several middleware and panics on a nil item, as `http.Handle` does. Three facts put a concern in middleware. It acts on one call. It needs neither the graph nor the state. It varies per application. Everything else stays in the core.

### Load control is middleware

The core has no concurrency limit and no rate limit. A limit in the saga cannot be shared across sagas and does not limit throughput. A semaphore or a rate limit is middleware that the application writes.

### Middleware cannot hide a failure

The pipeline keeps the result of the last call. If middleware returns nil after the call failed, the attempt ends with the call error. If the chain returns without a call, the attempt fails permanently with `ErrCallNotRun`. If a call is still running when middleware returns, the attempt fails with `ErrCallUnfinished` and its outcome is unknown. A second parallel call fails with `ErrCallConcurrent`. A late write lands in the state of its own attempt, which nothing reads after it ends.

### A panic is an ordinary error to middleware

The innermost handler turns a panic into a `*PanicError` with the value and the stack. Your middleware sees it as a normal error, and `Slog` can log it. A panic of the middleware itself is caught outside it.

### One typed error

Every error the library reports is a `*sagas.Error` with a `Kind`, a `Step`, the attempts, the cause and the earlier errors. The kinds are values that only the package creates, and `errors.Is` matches them. `Run` returns only an error: nil for completion, else a `*RunError` with the kind, the failed steps and the pending steps. A second return value gives two ways to read one outcome. The library never lets a second error replace the first. A caused error wraps the first, and an independent one joins it.

### The state names the execution

A state can have `ExecutionID() string`. Without it, `Run` draws a random ID. A caller option copies a value that the state already holds. The docs always use a business ID, because a random ID cannot recognize a repeated request.

### Enums are strings

Every exported enum is a string type whose value is its own lowercase text, such as `"forward-only"`. No `String` method is needed, and the stored text in a journal is readable. A table of names written by hand holds each name twice, and a missed row fails without an error.

## Testing

### `sagastest` takes a per-case setup

`Setup` returns a fresh `Case` for each case, with fresh fakes. A `Case` holds the saga, the state, a `Check`, the flow and `Late`. A function with four results breaks the lint rule. Each new field then breaks every setup.

### The helpers learn the steps from the flow

The helpers call `Inspect` on `Case.Flow`. A step that cannot start in its own case fails the test with its name and the reason. Finding steps from a clean run leaves a disabled step untested with no warning. If a clean run records no call, the helper fails once and names both causes.

### A late step is a hook

`Late` sends the action of a step again to your fakes with the idempotency key. This tests the third compensation case, a step that lands after its compensation. Two runs over shared fakes need an `ExecutionID`, and the second run compensates the step again.

### The twice case starts from an applied step

The compensation runs twice after an action that landed. A twice case from a step that never applied adds little to the never-applied case. It also lets a compensation pass although it is not idempotent over a landed effect.

### An unknown pivot with one attempt is valid

With one attempt, the run ends `ErrIncomplete` with only the pivot pending. This is a valid design that ends in manual work, so the helper does not fail it.

### No journal that stores and then fails

`sagastest` exports no journal that stores an event and fails afterward. Such a fault is ambiguous, and tests in the core already prove its handling. The journal contract holds the cases that a journal must meet.

### Examples use the external test package

A file that holds only `Example` functions declares the external test package. A reader who copies a published example then gets code that compiles, with the package named as a caller names it. Every other test file stays in the internal package.
