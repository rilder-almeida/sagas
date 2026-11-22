package sagastest

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rilder-almeida/sagas"
)

const (
	concurrentWriters = 8
	seqSecond         = 2
	seqThird          = 3
	mutated           = "mutated"

	ruleFresh          = "fresh journal"
	ruleNextEvent      = "next event"
	ruleGap            = "gap"
	ruleSeqBelowOne    = "seq below 1"
	ruleEqualResend    = "equal resend"
	ruleDifferentEvent = "different event"
	ruleRejectionFirst = "rejection at seq 1"
	ruleRejectionLater = "rejection at a later seq"
	ruleStoredCopy     = "stored copy"
	ruleConcurrent     = "concurrent writers"
	ruleDoneContext    = "done context"
	ruleInvalidEvent   = "invalid event"
	ruleTimeZone       = "time zone resend"
	ruleExactResend    = "exact resend"
)

// Journal proves the journal contract against any sagas.Journal. Each call of newJournal returns a fresh empty journal.
func Journal(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	if !freshRules(tb, newJournal) {
		return
	}

	sequenceRules(tb, newJournal)
	copyRules(tb, newJournal)
	concurrencyRules(tb, newJournal)
	contextRules(tb, newJournal)
	invalidEventRules(tb, newJournal)
	resendRules(tb, newJournal)
}

func journalEvent(seq int) *sagas.Event {
	return &sagas.Event{
		Saga: "saga", Execution: "execution", Writer: "writer", Seq: seq, Kind: sagas.EventStepStarted,
		Step: "step", Attempt: 1,
		Err:     sagas.EventError{Kind: "kind", Message: "message", Cause: "cause", Previous: []string{"previous"}},
		Changed: []string{"changed"}, Steps: []sagas.StepZone{{Name: "step", Zone: sagas.ZoneCompensable}},
		Time: time.Date(2026, time.November, 21, 12, 0, 0, 0, time.UTC),
	}
}

func otherEvent(seq int) *sagas.Event {
	e := journalEvent(seq)
	e.Step = "other"

	return e
}

func ruleFailedf(tb testing.TB, rule, format string, args ...any) {
	tb.Helper()
	tb.Errorf("sagastest: journal rule %q: "+format, append([]any{rule}, args...)...)
}

func wantNil(tb testing.TB, rule, what string, err error) {
	tb.Helper()

	if err != nil {
		ruleFailedf(tb, rule, "%s returned %v, want nil", what, err)
	}
}

func wantError(tb testing.TB, rule, what string, err error) {
	tb.Helper()

	if err == nil {
		ruleFailedf(tb, rule, "%s returned nil, want an error", what)
	}
}

func wantSentinel(tb testing.TB, rule, what string, err, sentinel error) {
	tb.Helper()

	if err != nil && !errors.Is(err, sentinel) {
		ruleFailedf(tb, rule, "%s returned %v, want an error wrapping %v", what, err, sentinel)
	}
}

func freshRules(tb testing.TB, newJournal func() sagas.Journal) bool {
	tb.Helper()

	first, second := newJournal(), newJournal()
	ctx := tb.Context()

	if err := first.Append(ctx, journalEvent(1)); err != nil {
		ruleFailedf(tb, ruleFresh, "Append of seq 1 on the first journal returned %v, want nil", err)

		return false
	}

	if err := second.Append(ctx, otherEvent(1)); err != nil {
		ruleFailedf(
			tb,
			ruleFresh,
			"newJournal returned a journal that is not empty: Append of seq 1 on the second journal returned %v",
			err,
		)

		return false
	}

	return true
}

func sequenceRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	ctx := tb.Context()

	next := newJournal()
	wantNil(tb, ruleNextEvent, "Append of seq 1", next.Append(ctx, journalEvent(1)))
	wantNil(tb, ruleNextEvent, "Append of seq 2", next.Append(ctx, journalEvent(seqSecond)))

	gap := newJournal()
	wantNil(tb, ruleGap, "Append of seq 1", gap.Append(ctx, journalEvent(1)))
	wantError(tb, ruleGap, "Append of seq 3 after seq 1", gap.Append(ctx, journalEvent(seqThird)))

	empty := newJournal()
	wantError(tb, ruleGap, "Append of seq 2 on an empty journal", empty.Append(ctx, journalEvent(seqSecond)))

	low := newJournal()
	wantError(tb, ruleSeqBelowOne, "Append of seq 0", low.Append(ctx, journalEvent(0)))
	wantError(tb, ruleSeqBelowOne, "Append of seq -1", low.Append(ctx, journalEvent(-1)))

	resend := newJournal()
	wantNil(tb, ruleEqualResend, "Append of seq 1", resend.Append(ctx, journalEvent(1)))
	wantNil(tb, ruleEqualResend, "Append of an equal copy of seq 1", resend.Append(ctx, journalEvent(1)))

	different := newJournal()
	wantNil(tb, ruleDifferentEvent, "Append of seq 1", different.Append(ctx, journalEvent(1)))
	wantNil(tb, ruleDifferentEvent, "Append of seq 2", different.Append(ctx, journalEvent(seqSecond)))

	first := different.Append(ctx, otherEvent(1))
	wantError(tb, ruleDifferentEvent, "Append of another event at taken seq 1", first)
	wantSentinel(tb, ruleRejectionFirst, "Append of another event at taken seq 1", first, sagas.ErrExecutionExists)

	later := different.Append(ctx, otherEvent(seqSecond))
	wantError(tb, ruleDifferentEvent, "Append of another event at taken seq 2", later)
	wantSentinel(
		tb,
		ruleRejectionLater,
		"Append of another event at taken seq 2",
		later,
		sagas.ErrConcurrentWriter,
	)
}

type mutation struct {
	name   string
	mutate func(e *sagas.Event)
}

func copyMutations() []mutation {
	return []mutation{
		{"scalar field", func(e *sagas.Event) { e.Step = mutated }},
		{"Changed slice", func(e *sagas.Event) { e.Changed[0] = mutated }},
		{"Steps slice", func(e *sagas.Event) { e.Steps[0].Name = mutated }},
		{"Err.Previous slice", func(e *sagas.Event) { e.Err.Previous[0] = mutated }},
	}
}

func copyRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	ctx := tb.Context()

	for _, m := range copyMutations() {
		j := newJournal()
		e := journalEvent(1)
		wantNil(tb, ruleStoredCopy, "Append of seq 1", j.Append(ctx, e))

		m.mutate(e)
		wantError(tb, ruleStoredCopy, "resend of seq 1 after the caller changed its "+m.name, j.Append(ctx, e))
		wantNil(tb, ruleStoredCopy, "resend of an unchanged copy of seq 1 after the caller changed its "+m.name,
			j.Append(ctx, journalEvent(1)))
	}
}

func concurrencyRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	j := newJournal()
	ctx := tb.Context()
	errs := make([]error, concurrentWriters)

	var wg sync.WaitGroup

	for i := range errs {
		wg.Go(func() {
			e := journalEvent(1)
			e.Writer = strconv.Itoa(i)
			errs[i] = j.Append(ctx, e)
		})
	}

	wg.Wait()

	wins := 0

	var wrong error

	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, sagas.ErrExecutionExists):
			wrong = err
		default:
		}
	}

	if wins != 1 {
		ruleFailedf(tb, ruleConcurrent, "%d of %d appends of seq 1 by different writers returned nil, want exactly 1",
			wins, concurrentWriters)
	}

	wantSentinel(tb, ruleConcurrent, "a losing append of seq 1", wrong, sagas.ErrExecutionExists)
}

func contextRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	j := newJournal()
	done, cancel := context.WithCancel(tb.Context())

	cancel()

	wantError(tb, ruleDoneContext, "Append of seq 1 with a done context", j.Append(done, journalEvent(1)))
	wantNil(tb, ruleDoneContext, "Append of another event at seq 1 after a done context",
		j.Append(tb.Context(), otherEvent(1)))
}

func invalidEventRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	j := newJournal()

	wantError(tb, ruleInvalidEvent, "Append of a nil event", j.Append(tb.Context(), nil))
	wantError(tb, ruleInvalidEvent, "Append of an event with seq 0", j.Append(tb.Context(), journalEvent(0)))
}

func resendRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	ctx := tb.Context()

	zoned := newJournal()
	wantNil(tb, ruleTimeZone, "Append of seq 1", zoned.Append(ctx, journalEvent(1)))

	again := journalEvent(1)
	again.Time = again.Time.In(time.FixedZone("zone", int(time.Hour/time.Second)))
	wantNil(tb, ruleTimeZone, "Append of an equal instant in another zone", zoned.Append(ctx, again))

	exact := newJournal()
	e := journalEvent(1)
	wantNil(tb, ruleExactResend, "Append of seq 1", exact.Append(ctx, e))
	wantNil(tb, ruleExactResend, "Append of the same event again", exact.Append(ctx, e))
}
