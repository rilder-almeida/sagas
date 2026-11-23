// Package sagastest holds test helpers for steps and journals built on package sagas: [Fail], [Unknown], [Panic] and
// [Recorder]. [Compensations] and [Pivot] prove the contracts of a saga. Their Setup must pass mw to
// sagas.WithMiddleware. [Journal] proves the contract of a journal. Each call of newJournal must return a fresh, empty
// journal.
package sagastest
