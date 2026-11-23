# Design decisions

This page explains why the library looks the way it does. Each entry gives the decision, the reason, and the main alternative that the design rejects. The page groups the entries by topic. The [guarantees](guarantees.md) page describes the promises that follow from these choices. The [glossary](../glossary.md) defines the core terms. This page explains each other term that it needs where the term first appears.

## The shape of the library

### A breaking rewrite

The library replaces the whole old API and ships as `v0.1.0`. The module path stays the same. The old API hid compensation, which is the central idea of the pattern, so a fix of its internals was not enough. Current users must migrate. The old tags, such as `v.0.0.11a`, do not follow semantic versioning. They stay in place, because deleting a published tag breaks users who depend on it.

### A definition apart from its runs

`sagas.New` builds and checks an immutable definition of the steps, their order and the options, and returns `(*Saga[S], error)`. `Run` starts one execution of it. Many executions can run at the same time. The rejected design was one mutable saga object that you configure and run in place. That object cannot run twice at once. Every problem in a definition comes back as an error from `New`, and `New` never panics.

### One typed state

A saga is generic over one state type `S`. Every action and compensation takes `*S`. The rejected design passed a typed output from each step to the steps that depend on it. In a graph of steps, the outputs have different types, so that design ends in `any` and type assertions. A `map[string]any` is worse, because its errors appear only at run time.

### The standard library only

The core module imports only the Go standard library. The repository ships the ports that the core owns, and no adapter for a vendor. A port is a small interface that the core defines and that you implement, such as `Journal`. An adapter for a transport, a store or a telemetry system lives outside this repository. The design rejects two options. Vendor modules in this repository carry vendor code and its upkeep. Integrations inside the core make every user download every client library. For the same reason, the library uses `errors` and `log/slog` from the standard library, and no third-party error or log package. Tests use `testify`, which is a test-only dependency.

### Go 1.27

The module requires Go 1.27, and CI runs on it. The library needs it because the state rules use the `encoding/json/v2` package of that release. Go supports its two newest releases. The rejected option was to require Go 1.26, which lets users of both releases import the module. Users on Go 1.26 cannot import the module.

### One way to do each thing

Each task has one way in the public API. Options that configure a saga carry the `With` prefix, such as `WithJournal`, `WithMiddleware` and `WithRecoveryTimeout`. The steps declare the content of a saga, so they carry no prefix. The design rejects two rules. "Never use `With`" makes option names collide with the `Journal` and `Middleware` types. "Use `With` only on a collision" makes each new option guess. A new feature that adds a second way to do a task needs a decision that removes one of the two ways.

### Defaults you can read

Every configurable value has an exported constant for its default, such as `DefaultAttempts` and `DefaultRecoveryTimeout`. A zero value means that default, so `RetryPolicy{}` is the default policy. Fields have names that make this work, such as `NoJitter`. The rejected option was a default variable, because any package can change it for the whole program.

### Option types are function types

`Option` is a function type with an unexported parameter. Callers write the same code as with an interface. The rejected interface form needed an exception to the `ireturn` lint rule. That rule rejects a function that returns an interface. The package uses the pattern nowhere else. A nil option, or an option given twice, is an error from `New`. The reason is that "last one wins" makes the first line lie to the reader.

## Steps and the flow

### A step is a type that embeds `sagas.Step`

A step is a pointer to your own type. The type embeds `sagas.Step`, which holds `Name`, `Description`, `Retry`, `AttemptTimeout` and `CompensationFailure`. The type has `Execute` or `Pivot`, and can have `Compensate`, `Enabled` and `Wrap`. The methods show what a step can do. The rejected design used functional options on a step. Closures spread over the options, and each option needed its own repeat check. A step with an empty `Name` takes its name from the type, so `CheckFraud` becomes `check-fraud`. A saga with a durable journal sets `Name`, so a rename of the type keeps the stored names.

The optional `Wrap` method is middleware for that one step. It runs inside the middleware of the saga, next to the call. The section on middleware below says more.

### `Sequence` and `Parallel` order the steps

`sagas.Sequence` and `sagas.Parallel` take steps or groups and return a `Flow`, the value that `New` takes as its second argument. One step is also a `Flow`. `Sequence` runs its items one after another. `Parallel` runs its items together. One pointer is one step, so a step that appears in two places runs once, with the dependencies of both places. You can write any graph without a cycle this way. The rejected design was an `After` list of step names. It repeats names across steps. A typo in a name does not fail at compile time. It fails at run time, in `New`.

### Names use a short ASCII set

A saga name, a step name and an execution ID hold only ASCII letters, digits, `-`, `_` and `.`. They start with a letter or a digit. Log and metric systems change other characters. `Mermaid` is the `Saga` method that draws the saga as a Mermaid diagram. It writes each name as it is. The design therefore rejects Unicode letters.

### A saga needs at least one step, and nothing nil

`New` rejects a saga with no steps, because the run reports a success that did nothing. It also rejects a nil `Option`, a nil item in the flow, a nil middleware and a nil journal. A nil value that the library accepts fails later, at call time, and becomes an unknown outcome. A typed nil inside a journal value counts as nil too.

### `Inspect` reports a flow the way `New` does

`Inspect` runs the same checks as `New` and returns one report for each step. The report holds the name, the settings, one flag for each step method and the resolved dependencies. `New` calls `Inspect`, so both return the same errors. `New` rejects a step type that gets one method from two embedded fields, at every depth, as the Go selector rules treat it. Without that rule, a method silently disappears from the step. The design rejects an exported `Compensator` interface, because it covers one method, and users need one function that checks the whole flow.

### A conditional step does not start

`Enabled(*S) bool` runs once, after the dependencies of the step finish. The library records the result and never asks again. A skipped step never started, so the library never compensates it, and the steps after it still run. The rejected options were a special `ErrSkip` error and a plain `return nil` inside the action. The first starts the step. The second compensates a step for nothing and reports a false success. The goroutine of the step runs `Enabled`, so a slow `Enabled` cannot stall the other steps.

## Compensation

### Every started step gets its compensation

A step that succeeded, failed, timed out or stopped on a cancel gets a compensation. A skipped step does not. A call that timed out can still land outside later. The rejected design compensates only completed steps. It never refunds a charge that the bank accepts late.

### Compensation follows the reversed graph

A compensation starts after the compensations of the started steps that depended on its step. Independent branches compensate in parallel. The rejected design ran one compensation at a time in a fixed order. It is slower, and the order between independent steps is arbitrary. A business order belongs in the flow, so no sequential-compensation option exists.

### Stop the siblings, then compensate

On the first failure or unknown outcome, the library cancels the running steps, starts nothing new and waits for every step goroutine. Only then do compensations start. The design rejects two alternatives. Letting siblings (steps that run in parallel) finish creates real effects that the library then undoes. Compensating each branch as it resolves runs compensations while forward steps still write the state, and the journal mixes forward work and compensation. A step that ignores its context blocks `Run`, and the contract says so.

### The step sets its failure policy

`CompensationFailure` on a step picks the policy for a failed compensation. `CompensationBlock` is the default. It stops every later compensation along the reversed graph, across steps that have no compensation, and lets independent branches go on. `CompensationHalt` stops all remaining compensations. `CompensationContinue` stops nothing. There is no saga-wide default, because one fixed behavior cannot fit every architecture, and the author of each step knows its risk. A policy on a step without `Compensate` is an error. `CompensationContinue` is an explicit departure from the classic model, kept for systems where undoing the rest is worth more than stopping.

### The library decides each compensation at its turn

The library decides a compensation after every compensation that it waits on ends. An earlier halt wins, then a block, and otherwise the compensation runs. The rejected design marked a compensation blocked at the moment of the failure. With two failures, the events of a later step then depend on timing.

## The pivot

### `New` enforces the pivot rules

`New` enforces at most one pivot. Every other step comes before or after it. The pivot has no compensation and no `Enabled`. No step after the pivot has a compensation. Real sagas often have a point of no return, and no Go library enforced these rules. The pivot must be atomic, and the forward-only steps must be idempotent. Both facts are in the contract, and `sagastest` exercises them.

### One kind of step

The library has no separate "retriable" step kind. The 1992 paper uses that name for a step that must succeed through retries. The pivot is a step with `Pivot`, and the forward-only zone carries its rules. A retriable kind adds a term for a position that the graph already shows.

### The pivot retries on a context that a cancel does not stop

The whole pivot call runs in one retry loop that the caller's cancel does not stop. The rejected design used two loops, one on the caller's context and one detached. It breaks the single retry loop and needs a rule to count attempts across them. The library does not retry a pivot that panics. A panic is a code defect, and a retry repeats it.

### A pivot failure that the journal cannot record

A pivot that failed counts as failed. A journal that cannot record the failure does not change this. The library knows that the pivot did not apply. It then compensates the compensable zone. Keeping the pivot unknown stops the run half way for no reason. A pivot that succeeded but whose commit failed counts as unknown, because it did apply and the saga cannot undo it.

## Retries and time

### One retry policy on the step

`Retry` on the step governs the action and the compensation. The rejected design added a saga-wide retry option, which is a second way to set the same thing. The policy is a struct with `Attempts`, `Initial`, `Max`, `Multiplier` and `NoJitter`. The defaults are three attempts, 200 ms initial delay, 30 s maximum delay, a multiplier of 2 and jitter. `NoRetry()` is a function that returns one attempt, so no caller can change a shared value.

### One stop rule

A retry loop stops at the first of four events. The attempts run out. An error carries the `Permanent` mark. The call panics. The deadline passes. The mark counts at any depth of the error chain. The rejected rule counts only the top level, which breaks a caller who wraps the mark with `%w`.

### The attempt timeout covers the action only

`AttemptTimeout` bounds each attempt of the action. A compensation attempt has no timeout of its own. The grace period, which `RecoveryTimeout` sets, bounds every compensation once the caller's context ends. The design rejects a second option for the same idea. Middleware can still cut each compensation attempt, because it reads the phase.

### One grace period for recovery

While the caller's context lives, the saga runs. After the caller gives up, recovery gets `RecoveryTimeout` from that moment, five minutes by default. This time is the grace period. Recovery is the work after a failure, an unknown outcome, a cancel or a journal fault. If the run stops before the pivot applies, backward recovery runs the compensations in reverse order. If another `Run` call appended to the execution first, backward recovery does not run them. After a cancel, once the pivot started, forward recovery goes forward instead of undoing anything. It runs the started pivot to its end. If the pivot fails, it did not apply, so backward recovery runs. If the pivot succeeds, forward recovery finishes the forward-only zone. A failure, an unknown outcome or a journal fault in that zone, or the end of the grace period, stops the run, and the rest stays pending. The design rejects two clocks. One clock starts at each phase, so the total is the sum of the phases. The other starts at the first failure, unknown outcome or pivot. Its time depends on the phase, so the caller cannot state one bound. The design also rejects stopping the forward-only zone on a cancel, because that creates manual work.

### The grace end has a name

The grace context is a context that the caller's cancel does not end. At the end of the grace period, it ends. The library then sets `ErrGraceEnded` as its cause. A caller tests for it with `errors.Is`. A plain cancel hides why recovery stopped. The library joins `ErrGraceEnded` to the error of each compensation and each journal append that the grace end stopped. The grace end can fail a compensation alone. The error then has the kind `ErrGraceEnded`. The kind `ErrFailed` says that the caller's code failed, and it did not. A late success still counts. A late compensation counts as compensated, and the library commits a late forward success. The library starts nothing new after the grace end.

### What matches the caller's cause

A caller can cancel its context with a reason through `context.WithCancelCause`, and `context.Cause(ctx)` returns that reason. The error of `Run` matches the cause of the caller's context only where the cancel interrupted work. If `ctx.Err()` and the cause differ, it matches both. The rejected rule matches after every cancel, so a real forward-only failure looks like a cancel.

### A late cancel does not undo finished work

If every step succeeded, the cancel came late and the journal recorded the end, the run completes. The rejected design compensates in that case, and it undoes a saga that already reached its goal.

## State and commits

### The state follows plain rules

The state is a closed set of plain types, and `New` names the field path and the rule of any state that breaks them. The rule list follows the JSON encoding of Go 1.27. The design rejects two other ways. One rejects every standard library type. That needs a list that the library must keep current. It also refuses types that come back unchanged after encoding and decoding, such as `time.Month`. The other encodes a sample value in `New` and decodes it again. That needs a generator for every hard case, and its error names a difference, not a rule.

### Attempts work on a private copy

Each attempt works on a private copy of the committed state. A successful attempt commits its changes at once, and an attempt that does not succeed drops the copy. The commit is a three-way merge of top-level fields. The merge compares three states field by field. They are the state at the start of the attempt, the copy of the attempt and the current committed state. Two concurrent changes to one field fail the later commit with `ErrConflict`. The design rejects a contract of "disjoint fields" that only the race detector checks, because a mistake then corrupts the state silently. It also rejects a lock for each step, which serializes the saga.

### A rejected run writes its state back

A writer is one `Run` call. After another writer stops the run mid-way, `Run` writes the state committed so far into your state value. The rejected design leaves the state as it was, and it hides an effect that really happened. If nothing ran, your state keeps its value.

## The journal

### One small port

`Journal` has one method, `Append(ctx, *Event)`. The library writes the intent before each action and compensation, and commits are events. A journal stores its own copy and never keeps the caller's pointer. The journal is not a queue. It never delivers, removes or marks events. The library has no messaging port today. A future messaging port must own its own acknowledgments.

### The journal fails closed

If the journal cannot write the intent, the action does not run. If the journal cannot write the outcome, the step is unknown, and the library compensates it before the pivot. A journal that stays down ends a run `ErrCompensationFailed` with `ErrJournal`. Going on in memory leaves silent gaps in the record. The library does not run its own retry loop for appends, because a journal implementation owns its own retries. It sends the exact failed event again once, before its next append, as a later entry describes.

### A single writer

Each event has a sequence number and a writer token. The writer token is a random value that each `Run` makes once. The journal appends only the next number. An exact resend by the same writer succeeds, so a lost reply does not stop a healthy run. Any other event at a taken number is a rejection. This rule is a condition on the write, not an acknowledgment. A second writer on a running execution stops the run without compensation, because that writer owns the execution now. The rejected design lets the journal assign the numbers and keeps an explicit ownership claim. That adds two concepts, the claim and the numbers that the journal assigns, and a larger contract for every journal.

### Events build the result

The saga-started event, the first journal event of a run, carries every step name, its zone, and whether the step has a compensation. Each `StepZone` holds these facts, the last one in its `Compensable` field. A blocked compensation and a halted compensation each have their own event. The library builds the `RunError` only from the events that it appended, so the journal alone holds what a reader needs to rebuild the outcome. The rejected design also reads the saga definition. Then the journal stops being the source of the outcome. The library also resends the exact event of a failed append before it writes anything new. This repairs the case where the first write landed and its reply was lost.

### The run error can owe more than the journal

After an append whose result is unknown, `Pending` can list more than the journal shows, and never less. The journal never holds two outcomes for one step. The rejected design changes each earlier decision to match the event that landed. For example, the library already compensated step A because step B looked unknown. If B's event lands later as a success, that design must undo the compensation of A. The library keeps the decision it made.

### Appends honor their context

Every append gets the grace context, which the journal honors. A stuck journal then cannot hold `Run` past the grace period. The last events can be missing in that case, and the error matches `ErrJournal`.

## Middleware and errors

### The core is a fixed pipeline

Each attempt of a step runs through a pipeline of private middleware in one order. The order is retry, timeout, panic recovery, a guard, your middleware, the `Wrap` method of the step, and the call. The guard fails an attempt whose chain never ran the call. The timeout layer applies to the action only. Panic recovery here means catching a panic, and it differs from the recovery after a failure or an unknown outcome. A step without `Wrap` skips that layer. One function builds it. The core does not export these pieces, because the guarantees must not depend on a user who installs them. Your middleware wraps every attempt and counts in the attempt timeout.

### Middleware is a value with `Wrap`

`Middleware` is an interface with `Wrap(ctx, info, call)`. The call data arrives as the `CallInfo` parameter, and middleware never receives the state. A function with the same signature adapts through `MiddlewareFunc`. A step method reads the idempotency key with `IdempotencyKey(ctx)`, and middleware reads `info.IdempotencyKey()`. `Chain` joins several middleware and panics on a nil item, as `http.Handle` does. Three facts put a concern in middleware. It acts on one call. It needs neither the graph nor the state. It varies per application. Everything else stays in the core.

### Load control is middleware

The core has no concurrency limit and no rate limit. A limit inside one saga serves that saga only, and it does not limit throughput. A semaphore or a rate limit is middleware that the application writes.

### Middleware cannot hide an error of the call

The pipeline keeps the result of the last call. If middleware returns nil after the call returned an error, the attempt ends with the error of the call. If the chain returns without a call, the attempt fails permanently with `ErrCallNotRun`. If a call is still running when middleware returns, the attempt fails with `ErrCallUnfinished` and its outcome is unknown. A second parallel call fails with `ErrCallConcurrent`. A late write lands in the state of its own attempt, which nothing reads after it ends.

### A panic is an ordinary error to middleware

The innermost handler turns a panic into a `*PanicError` with the value and the stack. Your middleware sees it as a normal error, and `Slog` can log it. `Slog` is the middleware of the library that logs each call through `log/slog`. A panic-recovery layer outside your middleware catches a panic of the middleware itself.

### One typed error for every step outcome that is not a success

Every error of a step is a `*sagas.Error`. That covers a failure and an unknown outcome. It holds a `Kind`, a `Step`, the attempts, the cause and the earlier errors. The kinds are values that only the package creates, and `errors.Is` matches them. `New` wraps `ErrInvalidStep`, `ErrDuplicateStep`, `ErrCycle` or `ErrInvalidState` for definition problems. `Run` returns only an error. Completion gives nil. If the journal has not stored the saga-started event yet, the error returns as it is. Examples are `ErrInvalidRun`, `ErrInvalidState`, `ErrExecutionExists` or an `ErrJournal` error. Every other end gives a `*RunError` with the kind, the pending steps, and one `*sagas.Error` for each action or compensation that did not succeed. A second return value gives two ways to read one outcome. The library never lets a second error replace the first. A caused error wraps the first, and an independent one joins it.

### The state names the execution

A state can have `ExecutionID() string`. Without it, `Run` draws a random ID. The rejected design was a `Run` option for the ID. It copies a value that the state already holds. The docs always use a business ID, because a random ID cannot recognize a repeated request.

### Enums are strings

Every exported enum is a string type whose value is its own lowercase text, such as `"forward-only"`. The enums need no `String` method, and the stored text in a journal is readable. The rejected design used integers with a table of names written by hand. That table holds each name twice, and a missed row fails without an error.

## Testing

### `sagastest` takes a per-case setup

`Setup` returns a fresh `Case` for each case, with fresh fakes. A fake is a test double of an outside service. A `Case` holds the saga, the state, a `Check`, the `Flow` and `Late`. The guarantees page describes each field. The rejected design returned each part as a separate result. A function with four results breaks the `function-result-limit` lint rule, which caps a function at three results. Each new field then breaks every setup.

### The helpers learn the steps from the flow

The helpers call `Inspect` on `Case.Flow`. A step that cannot start in its own case fails the test with its name and the reason. The rejected design finds the steps from a clean run. A disabled step then stays untested with no warning. If a clean run records no call, the helper fails once and names both causes: a setup that drops the middleware, or a flow whose steps are all disabled.

### A late action is a hook

`Late` sends the action of a step again to your fakes with the idempotency key, as a late call. This tests the third compensation case, an action that lands late, after its compensation. The rejected design runs the saga twice over shared fakes. That needs an `ExecutionID` on the state, and the second run compensates the step again.

### The twice case starts from an applied step

The compensation runs twice after an action that landed. The rejected design starts from a step that never applied. That case adds little to the never-applied case. It also lets a compensation pass although it is not idempotent over a landed effect.

### An unknown pivot with one attempt is valid

With one attempt, the run ends `ErrIncomplete` with only the pivot pending. This is a valid design that ends in manual work, so the helper does not fail it. The rejected design fails the test in that case, and it refuses a valid design.

### No journal that stores and then fails

`sagastest` exports no journal that stores an event and fails afterward. Such a fault is ambiguous, because the caller cannot tell whether the journal stored the event. Tests in the core already prove its handling. The rejected design adds a wrapper journal that injects the fault. It grows the public API and repeats what the core proves. The journal contract holds the cases that a journal must meet.

### Examples use the external test package

A file that holds only `Example` functions declares the external test package. A reader who copies a published example then gets code that compiles, with the package named as a caller names it. The rejected design keeps examples in the internal package. A copied example then does not compile outside the package. Every other test file stays in the internal package.
