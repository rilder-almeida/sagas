# Changelog

This file lists the notable changes of each release. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0]

This release rewrites the library. The API breaks with every earlier tag. Code that uses an earlier tag does not compile against this release.

### Removed

- The old API: `Action`, `Notifier` and `Observer`, with `ExecutionPlan`, `Retrier`, `Classifier` and their constructors. The old `Saga` and `Step` interfaces also go, and new types of the same names replace them.

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
- `Mermaid()` draws a saga as a Mermaid flowchart. The label of a step with `Enabled` ends in `(if Enabled)`.
- `sagas.Inspect` runs the checks of `New` on a flow and reports each step.
- The `sagastest` package: contract helpers, fault injection and a call recorder for your own saga.
- The documents under `docs/`: a glossary, a tutorial, two how-to guides and two explanation pages.

[Unreleased]: https://github.com/rilder-almeida/sagas/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/rilder-almeida/sagas/releases/tag/v0.1.0
