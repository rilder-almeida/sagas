// Package sagastest holds test helpers for steps and journals built on package sagas: [Fail], [Unknown], [Panic] and
// [Recorder]. [Compensations] and [Pivot] prove a saga's contracts; their Setup must pass mw to
// sagas.WithMiddleware. [Journal] proves a journal's; each call of newJournal must return a fresh, empty journal.
package sagastest
