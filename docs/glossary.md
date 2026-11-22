# Glossary

This glossary is the vocabulary of the library. Each term has one meaning in the code, the tests, the errors and the documents. The last column names the code that carries the term.

| Term | Meaning | Code name |
|---|---|---|
| Saga | A business operation split into steps. Either every step completes, or the completed part is undone. Past the pivot, the saga finishes instead. | `*sagas.Saga[S]` |
| Definition | The checked, fixed description of a saga: its steps, their order and its options. You build it once and run it many times. | `sagas.New[S]` |
| Execution | One run of a definition over one state value. An execution ID names it. | `(*Saga[S]).Run` |
| Execution ID | The name of one execution. The state sets it, so a repeated request is recognizable. Without it, the library draws a random one. | `ExecutionID() string` on the state, else random |
| State | The typed value that an execution reads and writes. All steps and compensations share it. | `*S` |
| Step | A named unit of work. Most steps change something outside the saga. A step can also only read. It has a name, an action and settings. | a type that embeds `sagas.Step` |
| Action | The method that a step runs forward. | `Execute(ctx, *S) error` |
| Dependency | A step that must finish before another step starts. | the order of `sagas.Sequence`, read back in `StepReport.After` |
| Outcome | How one attempt of a step ended: success, failure or unknown. | `EventStepSucceeded`, `EventStepFailed`, `EventStepUnknown` |
| Success | The call returned `nil`. | `nil` |
| Failure | The call returned an error that is not a timeout, a cancel or a panic. | `*sagas.Error`, usually of kind `ErrFailed`. A failed commit carries `ErrConflict` or `ErrInvalidState`. |
| Unknown | The call timed out, stopped on a cancel or panicked. Nobody knows whether the effect outside happened. | `*sagas.Error` of kind `ErrUnknown`, or `ErrPanic` for a panic |
| Compensation | The method that undoes the effect of a step in business terms. It adds a new fact and does not erase the old one. It runs for every started step of the compensable zone that has one. | `Compensate(ctx, *S) error` |
| Compensable step | A step in the compensable zone that has a compensation. | a step with `Compensate`, in `ZoneCompensable` |
| Step without compensation | A step in the compensable zone that has nothing to undo, such as a validation or a query. | a step with no `Compensate` |
| Pivot | The point of no return. It is atomic: it applies whole or not at all. A failure means that it did not apply. After it succeeds, the saga only moves forward. | `Pivot(ctx, *S) error`, `ZonePivot` |
| Compensable zone | The steps before the pivot. In a saga with no pivot, every step. A failure here undoes them. | `ZoneCompensable` |
| Forward-only zone | The steps after the pivot. A failure here never undoes anything. | `ZoneForwardOnly` |
| Backward recovery | Running compensations in reverse order after a failure in the compensable zone. | `PhaseCompensate` |
| Forward recovery | Finishing the forward-only zone instead of undoing anything. | no code name |
| Retry policy | How many attempts a call gets and how long to wait between them. One policy type serves every action and compensation. | `sagas.RetryPolicy`, in `Step.Retry` |
| Idempotency key | A key that is unique for each execution and step. It is the same on every attempt and on the compensation. The step sends it to the outside service. | `sagas.IdempotencyKey(ctx)`, `CallInfo.IdempotencyKey()` |
| Attempt | One call of an action or of a compensation. | `CallInfo.Attempt` |
| Permanent error | An error that stops the retries at once. | `sagas.Permanent(err)` |
| Journal | The append-only record of what an execution intends and did. The library writes each intent before the call. | `sagas.Journal` |
| Event | One record in the journal. The library never consumes, removes or acknowledges it. | `sagas.Event` |
| Expected sequence | The condition on a journal write. The journal refuses an event whose sequence number does not follow the last one of its execution. An exact resend by the same `Run` call is the one exception. The condition keeps one writer per execution. It is not an acknowledgment. | `Event.Seq` |
| Commit | Applying the state changes of a successful attempt to the committed state at once. The committed state is the state that the successful attempts so far changed. Each commit is a journal event. | no code name |
| Conflict | Two concurrent steps changed the same top-level field of the state. The later commit fails. | `ErrConflict` |
| Handler | The function that runs one action or one compensation. | `sagas.Call` |
| Middleware | A value that wraps a handler to add behavior, such as logging, a limit or fault injection. | `sagas.Middleware` |
| Result | What `Run` reports: `nil` for a completed run, else a `*RunError` that names the kind of end, the failed steps and the pending steps. A run that did not begin returns its own error, such as `ErrInvalidRun` or `ErrExecutionExists`. | the error of `Run`: `nil` or `*sagas.RunError` |
| Completed | Every step succeeded, or the library skipped it. | `nil`, `StatusCompleted` |
| Compensated | A step failed before the pivot, or the pivot failed, and every compensation succeeded. | `ErrCompensated` |
| Compensation failed | A compensation did not succeed. A person must finish the undo. | `ErrCompensationFailed` |
| Incomplete | The saga passed the pivot and stopped before the end, or the outcome of the pivot stayed unknown. A person must finish it. The library never undoes it. | `ErrIncomplete` |
