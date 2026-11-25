package sagastest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
	racedSeqs         = 3
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
	ruleUnknownVersion = "unknown version"
	ruleEventReader    = "event reader"
	ruleStoredEvents   = "stored events"
	ruleNoEvents       = "no events"
)

// Journal proves the journal contract on a sagas.Journal that is also a sagas.EventReader. Each call of newJournal must
// return a fresh empty journal.
func Journal(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	if _, ok := newJournal().(sagas.EventReader); !ok {
		ruleFailedf(tb, ruleEventReader, "the journal does not implement sagas.EventReader")

		return
	}

	if !freshRules(tb, newJournal) {
		return
	}

	sequenceRules(tb, newJournal)
	readRules(tb, newJournal)
	copyRules(tb, newJournal)
	concurrencyRules(tb, newJournal)
	contextRules(tb, newJournal)
	invalidEventRules(tb, newJournal)
	resendRules(tb, newJournal)
}

func journalEvent(seq int) *sagas.Event {
	return &sagas.Event{
		Saga: "saga", Execution: "execution", Writer: "writer", Seq: seq, Version: sagas.EventVersion,
		Kind: sagas.EventStepStarted,
		Step: "step", Attempts: 1,
		Err:     sagas.EventError{Kind: "kind", Message: "message", Cause: "cause", Previous: []string{"previous"}},
		Changed: []string{"changed"},
		Steps: []sagas.StepZone{
			{Name: "step", Zone: sagas.ZoneCompensable, Compensable: true},
			{Name: "bare", Zone: sagas.ZonePivot},
		},
		Status: sagas.StatusCompleted,
		Time:   time.Date(2026, time.November, 21, 12, 0, 0, 0, time.UTC),
	}
}

func otherEvent(seq int) *sagas.Event {
	e := journalEvent(seq)
	e.Step = "other"

	return e
}

func emptySlicesEvent(seq int) *sagas.Event {
	e := journalEvent(seq)
	e.Kind = sagas.EventSagaFinished
	e.Changed, e.Steps, e.Err.Previous = []string{}, []sagas.StepZone{}, []string{}

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

func sameEvent(got, want *sagas.Event) bool {
	a, b := *got, *want
	a.Time, b.Time = time.Time{}, time.Time{}
	nilEmptySlices(reflect.ValueOf(&a).Elem())
	nilEmptySlices(reflect.ValueOf(&b).Elem())

	return got.Time.Equal(want.Time) && reflect.DeepEqual(a, b)
}

func nilEmptySlices(v reflect.Value) {
	for _, f := range v.Fields() {
		switch {
		case f.Kind() == reflect.Slice && f.Len() == 0 && f.CanSet():
			f.SetZero()
		case f.Kind() == reflect.Struct && f.CanSet():
			nilEmptySlices(f)
		default:
		}
	}
}

func wantStored(tb testing.TB, rule string, j sagas.Journal, execution string, want ...*sagas.Event) {
	tb.Helper()

	reader, ok := j.(sagas.EventReader)
	if !ok {
		ruleFailedf(tb, rule, "the journal does not implement sagas.EventReader")

		return
	}

	got, err := reader.Events(tb.Context(), execution)
	if err != nil {
		ruleFailedf(tb, rule, "Events of execution %q returned %v, want nil", execution, err)

		return
	}

	if len(got) != len(want) {
		ruleFailedf(tb, rule, "Events of execution %q returned %d events, want %d", execution, len(got), len(want))

		return
	}

	for i := range want {
		if !sameEvent(&got[i], want[i]) {
			ruleFailedf(tb, rule, "Events of execution %q returned event %d as %+v, want %+v",
				execution, i+1, got[i], *want[i])
		}
	}
}

func readRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	ctx := tb.Context()

	j := newJournal()
	wantNil(tb, ruleStoredEvents, "Append of seq 1", j.Append(ctx, journalEvent(1)))
	wantNil(tb, ruleStoredEvents, "Append of seq 2", j.Append(ctx, otherEvent(seqSecond)))
	wantNil(tb, ruleStoredEvents, "Append of seq 3", j.Append(ctx, emptySlicesEvent(seqThird)))
	wantStored(tb, ruleStoredEvents, j, "execution", journalEvent(1), otherEvent(seqSecond), emptySlicesEvent(seqThird))

	reader, ok := j.(sagas.EventReader)
	if !ok {
		return
	}

	events, err := reader.Events(ctx, "missing")
	if err == nil {
		ruleFailedf(tb, ruleNoEvents, "Events of execution %q returned %d events and a nil error, want %v",
			"missing", len(events), sagas.ErrNoEvents)

		return
	}

	wantSentinel(tb, ruleNoEvents, `Events of execution "missing"`, err, sagas.ErrNoEvents)
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
		{"Version", func(e *sagas.Event) { e.Version = "v9.0.0" }},
		{"Changed slice", func(e *sagas.Event) { e.Changed[0] = mutated }},
		{"Steps slice", func(e *sagas.Event) { e.Steps[0].Name = mutated }},
		{"Steps Compensable from true", func(e *sagas.Event) { e.Steps[0].Compensable = false }},
		{"Steps Compensable from false", func(e *sagas.Event) { e.Steps[1].Compensable = true }},
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
		wantStored(tb, ruleStoredCopy, j, "execution", journalEvent(1))
	}
}

func concurrencyRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	j := newJournal()

	for seq := 1; seq <= racedSeqs; seq++ {
		raceForSeq(tb, j, seq)
	}
}

func raceForSeq(tb testing.TB, j sagas.Journal, seq int) {
	tb.Helper()

	ctx := tb.Context()
	errs := make([]error, concurrentWriters)

	var wg sync.WaitGroup

	for i := range errs {
		wg.Go(func() {
			e := journalEvent(seq)
			e.Writer = strconv.Itoa(i)
			errs[i] = j.Append(ctx, e)
		})
	}

	wg.Wait()

	rejection := sagas.ErrConcurrentWriter
	if seq == 1 {
		rejection = sagas.ErrExecutionExists
	}

	wins := 0

	var wrong error

	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, rejection):
			wrong = err
		default:
		}
	}

	if wins != 1 {
		ruleFailedf(tb, ruleConcurrent, "%d of %d appends of seq %d by different writers returned nil, want exactly 1",
			wins, concurrentWriters, seq)
	}

	wantSentinel(tb, ruleConcurrent, fmt.Sprintf("a losing append of seq %d", seq), wrong, rejection)
}

func contextRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	j := newJournal()
	done, cancel := context.WithCancel(tb.Context())

	cancel()

	wantError(tb, ruleDoneContext, "Append of seq 1 with a done context", j.Append(done, journalEvent(1)))
	wantNil(tb, ruleDoneContext, "Append of another event at seq 1 after a done context",
		j.Append(tb.Context(), otherEvent(1)))

	reader, ok := j.(sagas.EventReader)
	if !ok {
		return
	}

	events, err := reader.Events(done, "execution")
	if err == nil {
		ruleFailedf(tb, ruleDoneContext,
			"Events of execution %q with a done context returned nil and %d events, want %v",
			"execution", len(events), done.Err())

		return
	}

	wantSentinel(tb, ruleDoneContext, `Events of execution "execution" with a done context`, err, done.Err())
}

func invalidEventRules(tb testing.TB, newJournal func() sagas.Journal) {
	tb.Helper()

	j := newJournal()

	wantError(tb, ruleInvalidEvent, "Append of a nil event", j.Append(tb.Context(), nil))
	wantError(tb, ruleInvalidEvent, "Append of an event with seq 0", j.Append(tb.Context(), journalEvent(0)))

	for _, version := range []string{"v0.1.0", "v9.0.0"} {
		e := journalEvent(1)
		e.Version = version
		what := fmt.Sprintf("Append of an event with version %q", version)
		wantError(tb, ruleUnknownVersion, what, j.Append(tb.Context(), e))
	}
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
