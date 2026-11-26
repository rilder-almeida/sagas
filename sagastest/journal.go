package sagastest

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
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
	executionMain     = "execution"
	executionEmpty    = "empty"
	executionMissing  = "missing"

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

// JournalCases checks that probe is a sagas.EventReader with no event, then returns one case for each rule of the
// journal contract, named after the rule. Each case needs a fresh empty journal. Each error wraps ErrJournalContract.
func JournalCases(ctx context.Context, probe sagas.Journal) (iter.Seq[Case[sagas.Journal]], error) {
	if err := freshRules(ctx, probe); err != nil {
		return nil, err
	}

	return slices.Values([]Case[sagas.Journal]{
		{Name: ruleNextEvent, run: nextEventCase},
		{Name: ruleGap, run: gapCase},
		{Name: ruleSeqBelowOne, run: seqBelowOneCase},
		{Name: ruleEqualResend, run: equalResendCase},
		{Name: ruleDifferentEvent, run: differentEventCase},
		{Name: ruleRejectionFirst, run: rejectionFirstCase},
		{Name: ruleRejectionLater, run: rejectionLaterCase},
		{Name: ruleStoredEvents, run: storedEventsCase},
		{Name: ruleNoEvents, run: noEventsCase},
		{Name: ruleStoredCopy, run: storedCopyCase},
		{Name: ruleConcurrent, run: concurrentCase},
		{Name: ruleDoneContext, run: doneContextCase},
		{Name: ruleInvalidEvent, run: invalidEventCase},
		{Name: ruleUnknownVersion, run: unknownVersionCase},
		{Name: ruleTimeZone, run: timeZoneCase},
		{Name: ruleExactResend, run: exactResendCase},
	}), nil
}

// TestJournal runs each case of JournalCases in a subtest named after it. It calls newJournal with t for the probe and
// with the t of the subtest for each case. Each call must return a fresh empty journal.
func TestJournal(t *testing.T, newJournal func(*testing.T) sagas.Journal) {
	t.Helper()

	cases, err := JournalCases(t.Context(), newJournal(t))
	if err != nil {
		t.Fatal(err)
	}

	runCases(t, cases, newJournal)
}

func journalEvent(seq int) *sagas.Event {
	return eventOf(executionMain, seq)
}

func eventOf(execution string, seq int) *sagas.Event {
	return &sagas.Event{
		Saga: "saga", Execution: execution, Writer: "writer", Seq: seq, Version: sagas.EventVersion,
		Kind: sagas.EventStepStarted,
		Step: "step", Attempts: 1,
		Err:    sagas.EventError{Kind: "kind", Message: "message", Cause: "cause", Previous: []string{"previous"}},
		Values: []sagas.FieldValue{{Field: "Field", Value: jsontext.Value(`"value"`)}, {Field: "Secret"}},
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
	e.Values, e.Steps, e.Err.Previous = []sagas.FieldValue{}, []sagas.StepZone{}, []string{}

	return e
}

func ruleFailedf(rule, format string, args ...any) error {
	return ruleFailure(ErrJournalContract, rule, fmt.Sprintf(format, args...), nil)
}

func wantNil(rule, what string, err error) error {
	if err != nil {
		return ruleFailure(ErrJournalContract, rule, what+" returned an error, want nil", err)
	}

	return nil
}

func wantError(rule, what string, err error) error {
	if err == nil {
		return ruleFailedf(rule, "%s returned nil, want an error", what)
	}

	return nil
}

func wantSentinel(rule, what string, err, sentinel error) error {
	if err != nil && !errors.Is(err, sentinel) {
		return ruleFailure(ErrJournalContract, rule,
			fmt.Sprintf("%s returned an error, want an error wrapping %v", what, sentinel), err)
	}

	return nil
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

func notReader(rule string) error {
	return ruleFailedf(rule, "the journal does not implement sagas.EventReader")
}

func wantStored(ctx context.Context, rule string, j sagas.Journal, execution string, want ...*sagas.Event) error {
	reader, ok := j.(sagas.EventReader)
	if !ok {
		return notReader(rule)
	}

	got, err := reader.Events(ctx, execution)
	if err != nil {
		return ruleFailure(ErrJournalContract, rule,
			fmt.Sprintf("Events of execution %q returned an error, want nil", execution), err)
	}

	if len(got) != len(want) {
		return ruleFailedf(rule, "Events of execution %q returned %d events, want %d", execution, len(got), len(want))
	}

	var errs []error

	for i := range want {
		if !sameEvent(&got[i], want[i]) {
			errs = append(errs, ruleFailedf(rule, "Events of execution %q returned event %d as %+v, want %+v",
				execution, i+1, got[i], *want[i]))
		}
	}

	return errors.Join(errs...)
}

func journalExecutions() []string {
	mutations := copyMutations()

	executions := make([]string, 0, len(mutations)+seqThird)
	executions = append(executions, executionMain, executionEmpty, executionMissing)

	for _, m := range mutations {
		executions = append(executions, m.name)
	}

	return executions
}

func freshRules(ctx context.Context, probe sagas.Journal) error {
	reader, ok := probe.(sagas.EventReader)
	if !ok {
		return notReader(ruleEventReader)
	}

	var errs []error

	for _, execution := range journalExecutions() {
		events, readErr := reader.Events(ctx, execution)

		switch {
		case readErr != nil && !errors.Is(readErr, sagas.ErrNoEvents):
			errs = append(errs, ruleFailure(ErrJournalContract, ruleFresh, fmt.Sprintf(
				"Events of execution %q returned an error, want an error wrapping %v", execution, sagas.ErrNoEvents),
				readErr))
		case len(events) > 0:
			errs = append(errs, ruleFailedf(ruleFresh, "Events of execution %q returned %d events, want 0",
				execution, len(events)))
		default:
		}
	}

	return errors.Join(errs...)
}

func nextEventCase(ctx context.Context, j sagas.Journal) error {
	return errors.Join(
		wantNil(ruleNextEvent, "Append of seq 1", j.Append(ctx, journalEvent(1))),
		wantNil(ruleNextEvent, "Append of seq 2", j.Append(ctx, journalEvent(seqSecond))),
	)
}

func gapCase(ctx context.Context, j sagas.Journal) error {
	return errors.Join(
		wantError(ruleGap, "Append of seq 2 on an empty journal", j.Append(ctx, journalEvent(seqSecond))),
		wantNil(ruleGap, "Append of seq 1", j.Append(ctx, journalEvent(1))),
		wantError(ruleGap, "Append of seq 3 after seq 1", j.Append(ctx, journalEvent(seqThird))),
		wantError(ruleGap, "Append of seq 2 on an empty execution", j.Append(ctx, eventOf(executionEmpty, seqSecond))),
	)
}

func seqBelowOneCase(ctx context.Context, j sagas.Journal) error {
	return errors.Join(
		wantError(ruleSeqBelowOne, "Append of seq 0", j.Append(ctx, journalEvent(0))),
		wantError(ruleSeqBelowOne, "Append of seq -1", j.Append(ctx, journalEvent(-1))),
	)
}

func equalResendCase(ctx context.Context, j sagas.Journal) error {
	return errors.Join(
		wantNil(ruleEqualResend, "Append of seq 1", j.Append(ctx, journalEvent(1))),
		wantNil(ruleEqualResend, "Append of an equal copy of seq 1", j.Append(ctx, journalEvent(1))),
	)
}

func takeTwoSeqs(ctx context.Context, j sagas.Journal, rule string) error {
	return errors.Join(
		wantNil(rule, "Append of seq 1", j.Append(ctx, journalEvent(1))),
		wantNil(rule, "Append of seq 2", j.Append(ctx, journalEvent(seqSecond))),
	)
}

func differentEventCase(ctx context.Context, j sagas.Journal) error {
	return errors.Join(
		takeTwoSeqs(ctx, j, ruleDifferentEvent),
		wantError(ruleDifferentEvent, "Append of another event at taken seq 1", j.Append(ctx, otherEvent(1))),
		wantError(ruleDifferentEvent, "Append of another event at taken seq 2", j.Append(ctx, otherEvent(seqSecond))),
	)
}

func rejectionFirstCase(ctx context.Context, j sagas.Journal) error {
	return errors.Join(
		takeTwoSeqs(ctx, j, ruleRejectionFirst),
		wantSentinel(ruleRejectionFirst, "Append of another event at taken seq 1", j.Append(ctx, otherEvent(1)),
			sagas.ErrExecutionExists),
	)
}

func rejectionLaterCase(ctx context.Context, j sagas.Journal) error {
	return errors.Join(
		takeTwoSeqs(ctx, j, ruleRejectionLater),
		wantSentinel(ruleRejectionLater, "Append of another event at taken seq 2", j.Append(ctx, otherEvent(seqSecond)),
			sagas.ErrConcurrentWriter),
	)
}

func storedEventsCase(ctx context.Context, j sagas.Journal) error {
	return errors.Join(
		wantNil(ruleStoredEvents, "Append of seq 1", j.Append(ctx, journalEvent(1))),
		wantNil(ruleStoredEvents, "Append of seq 2", j.Append(ctx, otherEvent(seqSecond))),
		wantNil(ruleStoredEvents, "Append of seq 3", j.Append(ctx, emptySlicesEvent(seqThird))),
		wantStored(ctx, ruleStoredEvents, j, executionMain,
			journalEvent(1), otherEvent(seqSecond), emptySlicesEvent(seqThird)),
	)
}

func noEventsCase(ctx context.Context, j sagas.Journal) error {
	setup := wantNil(ruleNoEvents, "Append of seq 1", j.Append(ctx, journalEvent(1)))

	reader, ok := j.(sagas.EventReader)
	if !ok {
		return errors.Join(setup, notReader(ruleNoEvents))
	}

	events, err := reader.Events(ctx, executionMissing)
	if err == nil {
		return errors.Join(setup, ruleFailedf(ruleNoEvents,
			"Events of execution %q returned %d events and a nil error, want %v",
			executionMissing, len(events), sagas.ErrNoEvents))
	}

	return errors.Join(setup, wantSentinel(ruleNoEvents, fmt.Sprintf("Events of execution %q", executionMissing), err,
		sagas.ErrNoEvents))
}

type mutation struct {
	name   string
	mutate func(e *sagas.Event)
}

func copyMutations() []mutation {
	return []mutation{
		{"scalar field", func(e *sagas.Event) { e.Step = mutated }},
		{"Version", func(e *sagas.Event) { e.Version = "v9.0.0" }},
		{"Values element", func(e *sagas.Event) { e.Values[0].Field = mutated }},
		{"Values byte", func(e *sagas.Event) { e.Values[0].Value[1] = 'x' }},
		{"Steps slice", func(e *sagas.Event) { e.Steps[0].Name = mutated }},
		{"Steps Compensable from true", func(e *sagas.Event) { e.Steps[0].Compensable = false }},
		{"Steps Compensable from false", func(e *sagas.Event) { e.Steps[1].Compensable = true }},
		{"Err.Previous slice", func(e *sagas.Event) { e.Err.Previous[0] = mutated }},
	}
}

func storedCopyCase(ctx context.Context, j sagas.Journal) error {
	mutations := copyMutations()
	errs := make([]error, 0, len(mutations))

	for _, m := range mutations {
		e := eventOf(m.name, 1)
		stored := wantNil(ruleStoredCopy, "Append of seq 1", j.Append(ctx, e))

		m.mutate(e)
		errs = append(errs, errors.Join(
			stored,
			wantError(ruleStoredCopy, "resend of seq 1 after the caller changed its "+m.name, j.Append(ctx, e)),
			wantNil(ruleStoredCopy, "resend of an unchanged copy of seq 1 after the caller changed its "+m.name,
				j.Append(ctx, eventOf(m.name, 1))),
			wantStored(ctx, ruleStoredCopy, j, m.name, eventOf(m.name, 1)),
		))
	}

	return errors.Join(errs...)
}

func concurrentCase(ctx context.Context, j sagas.Journal) error {
	errs := make([]error, 0, racedSeqs)

	for seq := 1; seq <= racedSeqs; seq++ {
		errs = append(errs, raceForSeq(ctx, j, seq))
	}

	return errors.Join(errs...)
}

func raceForSeq(ctx context.Context, j sagas.Journal, seq int) error {
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
	wrongs := make([]error, 0, concurrentWriters)

	var winners error

	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, rejection):
			wrongs = append(wrongs, err)
		default:
		}
	}

	if wins != 1 {
		winners = ruleFailedf(ruleConcurrent, "%d of %d appends of seq %d by different writers returned nil, want 1",
			wins, concurrentWriters, seq)
	}

	return errors.Join(winners, wantSentinel(ruleConcurrent, fmt.Sprintf("a losing append of seq %d", seq),
		errors.Join(wrongs...), rejection))
}

func doneContextCase(ctx context.Context, j sagas.Journal) error {
	done, cancel := context.WithCancel(ctx)

	cancel()

	setup := errors.Join(
		wantError(ruleDoneContext, "Append of seq 1 with a done context", j.Append(done, journalEvent(1))),
		wantNil(ruleDoneContext, "Append of another event at seq 1 after a done context",
			j.Append(ctx, otherEvent(1))),
	)

	reader, ok := j.(sagas.EventReader)
	if !ok {
		return errors.Join(setup, notReader(ruleDoneContext))
	}

	events, err := reader.Events(done, executionMain)
	if err == nil {
		return errors.Join(setup, ruleFailedf(ruleDoneContext,
			"Events of execution %q with a done context returned nil and %d events, want %v",
			executionMain, len(events), done.Err()))
	}

	return errors.Join(setup, wantSentinel(ruleDoneContext,
		fmt.Sprintf("Events of execution %q with a done context", executionMain), err, done.Err()))
}

func invalidEventCase(ctx context.Context, j sagas.Journal) error {
	return errors.Join(
		wantError(ruleInvalidEvent, "Append of a nil event", j.Append(ctx, nil)),
		wantError(ruleInvalidEvent, "Append of an event with seq 0", j.Append(ctx, journalEvent(0))),
	)
}

func unknownVersionCase(ctx context.Context, j sagas.Journal) error {
	versions := []string{"v0.1.0", "v9.0.0"}
	errs := make([]error, 0, len(versions))

	for _, version := range versions {
		e := journalEvent(1)
		e.Version = version
		what := fmt.Sprintf("Append of an event with version %q", version)
		errs = append(errs, wantError(ruleUnknownVersion, what, j.Append(ctx, e)))
	}

	return errors.Join(errs...)
}

func timeZoneCase(ctx context.Context, j sagas.Journal) error {
	again := journalEvent(1)
	again.Time = again.Time.In(time.FixedZone("zone", int(time.Hour/time.Second)))

	return errors.Join(
		wantNil(ruleTimeZone, "Append of seq 1", j.Append(ctx, journalEvent(1))),
		wantNil(ruleTimeZone, "Append of an equal instant in another zone", j.Append(ctx, again)),
	)
}

func exactResendCase(ctx context.Context, j sagas.Journal) error {
	e := journalEvent(1)

	return errors.Join(
		wantNil(ruleExactResend, "Append of seq 1", j.Append(ctx, e)),
		wantNil(ruleExactResend, "Append of the same event again", j.Append(ctx, e)),
	)
}
