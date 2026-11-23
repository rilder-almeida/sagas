# Changelog

This file lists the notable changes of each release. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-11-23

This release rewrites the library. The API breaks with every earlier tag. Most code that uses an earlier tag does not compile against this release.

### Added

- `sagas.New` builds a saga from a name, a `Flow` and functional options. It checks the flow before the first run.
- A step is a type that embeds `sagas.Step`. Its methods `Execute`, `Pivot`, `Compensate`, `Enabled` and `Wrap` give its behavior.
- `sagas.Sequence` and `sagas.Parallel` put the steps in order.
- The pivot and the two zones. `New` rejects a flow that breaks a pivot rule.
- One retry policy, `sagas.RetryPolicy`, for each action and compensation. `sagas.NoRetry()` makes one attempt, and `sagas.Permanent(err)` stops the retries at once.
- The grace period after the caller's context ends, set with `sagas.WithRecoveryTimeout`.
- The journal port, `sagas.Journal`, with an expected sequence on each append. Without `sagas.WithJournal`, each run uses a fresh in-memory journal.
- Middleware, a value with a `Wrap(ctx, info, call)` method. `sagas.Slog` logs each attempt.
- `sagas.IdempotencyKey(ctx)` for a step method and `CallInfo.IdempotencyKey()` for middleware.
- `Run` returns nil for a completed run and a `*sagas.RunError` for any other end that began.
- The saga-started event carries `StepZone.Compensable` for each step, so a journal records whether the step has a compensation.
- Every event carries `Version`, set to `sagas.EventVersion`. The version names the format of the event, which is its set of fields and their meaning. The library never reads stored events back. Your own code reads them, so it checks `Version` first and refuses a version that it does not know. A journal refuses an event with an unknown version.
- Version 1 events hold no state values, so a later release cannot resume a version 1 execution.
- The `sagas.EventReader` port reads the events of one execution back from a journal. `MemoryJournal` implements it, and `sagastest.Journal` requires it. If the context is done, `Events` returns an error that wraps the error of the context.
- `sagas.ErrNoEvents` reports that the journal holds no event for an execution. `EventReader.Events` returns it instead of an empty list.
- `Run` always starts a new execution. An execution ID that the journal of the saga already holds fails with `sagas.ErrExecutionExists`. Without `sagas.WithJournal`, each run gets a fresh journal, so a repeated ID does not fail.
- The compatibility policy in the README says what each kind of release can change.
- `Mermaid()` draws a saga as a Mermaid flowchart. The label of a step with `Enabled` ends in `(if Enabled)`.
- `sagas.Inspect` runs the checks of `New` on a flow and reports each step.
- The `sagastest` package: contract helpers, fault injection and a call recorder for your own saga.
- The documents under `docs/`: a glossary, a tutorial, two how-to guides and two explanation pages.

### Removed

- Every exported identifier of the old API, as tag `v.0.0.11a` has it:
  - `Action`, `ActionFn` and `NewAction`
  - `BackoffConstant`, `BackoffExponential` and `BackoffLimitedExponential`
  - `Classifier`, `NewClassifier`, `NewClassifierWhitelist` and `NewClassifierBlacklist`
  - `EnderFn`, `ExecutionPlan` and `NewExecutionPlan`
  - `Event`, `Identifier` and `NewIdentifier`
  - `Notification`, `NewNotification`, `Notifier` and `NewNotifier`
  - `Observer`, `NewObserver`, `Retrier` and `NewRetrier`
  - `Saga`, `NewSaga`, `Step` and `NewStep`
  - `State`, with `Idle`, `Running` and `Completed`
  - `Status`, with `Undefined`, `Failed` and `Successed`
- `Saga`, `Step`, `Event` and `Status` return as new types with a different meaning. Old code that uses one of these names can still compile, so read each use again. `Saga` was an interface and is now the generic struct `Saga[S]`. `Step` was an interface and is now a struct that a step type embeds. `Event` was an interface and is now a struct. `Status` was an `int` and is now a `string`.

[Unreleased]: https://github.com/rilder-almeida/sagas/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/rilder-almeida/sagas/releases/tag/v0.1.0
