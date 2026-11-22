// Package sagastest holds test helpers for steps and stores built on package sagas: fault injection middleware
// and a recorder of step calls. Compensations and Pivot prove a saga's contracts, Journal proves a journal's,
// and each call of Journal's newJournal must return a fresh, empty journal.
package sagastest
