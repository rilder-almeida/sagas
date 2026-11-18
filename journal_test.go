package sagas

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_EventKind_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind EventKind
		want string
	}{
		{name: "[Success] When: the zero kind ; Should: return invalid", kind: EventInvalid, want: "invalid"},
		{
			name: "[Success] When: EventSagaStarted ; Should: return saga started",
			kind: EventSagaStarted, want: "saga started",
		},
		{
			name: "[Success] When: EventStepStarted ; Should: return step started",
			kind: EventStepStarted, want: "step started",
		},
		{
			name: "[Success] When: EventStepSucceeded ; Should: return step succeeded",
			kind: EventStepSucceeded, want: "step succeeded",
		},
		{
			name: "[Success] When: EventStepFailed ; Should: return step failed",
			kind: EventStepFailed, want: "step failed",
		},
		{
			name: "[Success] When: EventStepUnknown ; Should: return step unknown",
			kind: EventStepUnknown, want: "step unknown",
		},
		{
			name: "[Success] When: EventStepSkipped ; Should: return step skipped",
			kind: EventStepSkipped, want: "step skipped",
		},
		{
			name: "[Success] When: EventCompensationStarted ; Should: return compensation started",
			kind: EventCompensationStarted, want: "compensation started",
		},
		{
			name: "[Success] When: EventCompensationSucceeded ; Should: return compensation succeeded",
			kind: EventCompensationSucceeded, want: "compensation succeeded",
		},
		{
			name: "[Success] When: EventCompensationFailed ; Should: return compensation failed",
			kind: EventCompensationFailed, want: "compensation failed",
		},
		{
			name: "[Success] When: EventCompensationBlocked ; Should: return compensation blocked",
			kind: EventCompensationBlocked, want: "compensation blocked",
		},
		{
			name: "[Success] When: EventCompensationHalted ; Should: return compensation halted",
			kind: EventCompensationHalted, want: "compensation halted",
		},
		{
			name: "[Success] When: EventSagaFinished ; Should: return saga finished",
			kind: EventSagaFinished, want: "saga finished",
		},
		{
			name: "[Fail] When: the kind is out of range ; Should: return EventKind(99)",
			kind: EventKind(99), want: "EventKind(99)",
		},
		{
			name: "[Fail] When: the kind is negative ; Should: return EventKind(-1)",
			kind: EventKind(-1), want: "EventKind(-1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.kind.String())
		})
	}
}

func Test_Zone_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		zone Zone
		want string
	}{
		{name: "[Success] When: the zero zone ; Should: return invalid", zone: ZoneInvalid, want: "invalid"},
		{
			name: "[Success] When: ZoneCompensable ; Should: return compensable",
			zone: ZoneCompensable, want: "compensable",
		},
		{name: "[Success] When: ZonePivot ; Should: return pivot", zone: ZonePivot, want: "pivot"},
		{
			name: "[Success] When: ZoneForwardOnly ; Should: return forward-only",
			zone: ZoneForwardOnly, want: "forward-only",
		},
		{name: "[Fail] When: the zone is out of range ; Should: return Zone(99)", zone: Zone(99), want: "Zone(99)"},
		{name: "[Fail] When: the zone is negative ; Should: return Zone(-1)", zone: Zone(-1), want: "Zone(-1)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.zone.String())
		})
	}
}

func journalEvent(execution string, seq int) Event {
	return Event{
		Saga:      "booking",
		Execution: execution,
		Writer:    "w1",
		Seq:       seq,
		Kind:      EventStepSucceeded,
		Step:      "reserve",
		Attempt:   1,
		Err:       "",
		Changed:   []string{"Seats"},
		Steps:     []StepZone{{Name: "reserve", Zone: ZoneCompensable}},
		Status:    StatusUnfinished,
		Time:      time.Date(2026, 11, 17, 10, 0, seq, 0, time.UTC),
	}
}

func withEvent(e *Event, change func(*Event)) *Event {
	out := *e
	out.Changed = slices.Clone(out.Changed)
	out.Steps = slices.Clone(out.Steps)
	change(&out)

	return &out
}

func scribble(events []Event) {
	for i := range events {
		scribbleEvent(&events[i])
	}
}

func scribbleEvent(e *Event) {
	e.Step = "scribbled"
	e.Seq = 99

	for k := range e.Changed {
		e.Changed[k] = "scribbled"
	}

	for k := range e.Steps {
		e.Steps[k].Name = "scribbled"
	}
}

type readableJournal interface {
	Journal
	events(execution string) []Event
}

type aliasingJournal struct {
	stored []*Event
}

func (j *aliasingJournal) Append(_ context.Context, e *Event) error {
	j.stored = append(j.stored, e)

	return nil
}

func (j *aliasingJournal) events(_ string) []Event {
	out := make([]Event, len(j.stored))
	for i, e := range j.stored {
		out[i] = *e
	}

	return out
}

type shallowJournal struct {
	stored []Event
}

func (j *shallowJournal) Append(_ context.Context, e *Event) error {
	j.stored = append(j.stored, *e)

	return nil
}

func (j *shallowJournal) events(_ string) []Event {
	return j.stored
}

type leak struct {
	scalar bool
	slice  bool
}

func callerChangeLeaks(t *testing.T, j readableJournal) leak {
	t.Helper()

	e := journalEvent("leak", 1)
	require.NoError(t, j.Append(t.Context(), &e))
	scribbleEvent(&e)

	stored := j.events("leak")
	require.Len(t, stored, 1)

	return leak{scalar: stored[0].Step == e.Step, slice: stored[0].Changed[0] == e.Changed[0]}
}

func Test_memoryJournal_Append_callerChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		journal readableJournal
		want    leak
	}{
		{
			name:    "[Success] When: memoryJournal and the caller changes the event ; Should: report no leak",
			journal: newMemoryJournal(),
			want:    leak{},
		},
		{
			name:    "[Fail] When: a journal keeps the caller's pointer ; Should: report a scalar and a slice leak",
			journal: &aliasingJournal{},
			want:    leak{scalar: true, slice: true},
		},
		{
			name:    "[Fail] When: a journal copies the event but shares its slices ; Should: report a slice leak only",
			journal: &shallowJournal{},
			want:    leak{slice: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, callerChangeLeaks(t, tt.journal))
		})
	}
}

func Test_memoryJournal_Append(t *testing.T) {
	t.Parallel()

	now := time.Now()
	e1, e2, e3 := journalEvent("a", 1), journalEvent("a", 2), journalEvent("a", 3)
	e5, e0, eNeg, b1 := journalEvent("a", 5), journalEvent("a", 0), journalEvent("a", -1), journalEvent("b", 1)
	c1 := journalEvent("c", 1)
	c1.Changed, c1.Steps = nil, nil
	stamped := withEvent(&e1, func(e *Event) { e.Time = now })

	tests := []struct {
		name    string
		prior   []Event
		event   *Event
		wantErr error
		wantMsg string
		want    map[string][]Event
	}{
		{
			name:  "[Success] When: the first event of a new execution ; Should: write it",
			event: &e1,
			want:  map[string][]Event{"a": {e1}},
		},
		{
			name:  "[Success] When: the next Seq ; Should: write it after the last event",
			prior: []Event{e1},
			event: &e2,
			want:  map[string][]Event{"a": {e1, e2}},
		},
		{
			name:  "[Success] When: the first event has nil Changed and Steps ; Should: store them as nil",
			event: &c1,
			want:  map[string][]Event{"c": {c1}},
		},
		{
			name:    "[Fail] When: Seq 5 after Seq 1 to 3 ; Should: return ErrConcurrentWriter and write nothing",
			prior:   []Event{e1, e2, e3},
			event:   &e5,
			wantErr: ErrConcurrentWriter,
			wantMsg: `sagas: concurrent writer: execution "a" seq 5`,
			want:    map[string][]Event{"a": {e1, e2, e3}},
		},
		{
			name:    "[Fail] When: Seq 0 on an empty execution ; Should: return errInvalidEvent and write nothing",
			event:   &e0,
			wantErr: errInvalidEvent,
			wantMsg: `sagas: invalid event: execution "a" seq 0`,
			want:    map[string][]Event{"a": {}},
		},
		{
			name:    "[Fail] When: Seq -1 after Seq 1 ; Should: return errInvalidEvent and write nothing",
			prior:   []Event{e1},
			event:   &eNeg,
			wantErr: errInvalidEvent,
			wantMsg: `sagas: invalid event: execution "a" seq -1`,
			want:    map[string][]Event{"a": {e1}},
		},
		{
			name:    "[Fail] When: a nil event after Seq 1 ; Should: return errInvalidEvent and write nothing",
			prior:   []Event{e1},
			wantErr: errInvalidEvent,
			wantMsg: `sagas: invalid event: nil event`,
			want:    map[string][]Event{"a": {e1}},
		},
		{
			name:  "[Success] When: an equal resend, new slices, monotonic-only Time ; Should: nil, no write",
			prior: []Event{*stamped},
			event: withEvent(stamped, func(e *Event) { e.Time = now.Round(0) }),
			want:  map[string][]Event{"a": {*stamped}},
		},
		{
			name:  "[Success] When: an equal resend of an earlier Seq ; Should: return nil, write nothing",
			prior: []Event{e1, e2, e3},
			event: withEvent(&e2, func(*Event) {}),
			want:  map[string][]Event{"a": {e1, e2, e3}},
		},
		{
			name:    "[Fail] When: the same Seq and Writer with another Kind ; Should: return ErrConcurrentWriter",
			prior:   []Event{e1, e2},
			event:   withEvent(&e2, func(e *Event) { e.Kind = EventStepFailed }),
			wantErr: ErrConcurrentWriter,
			wantMsg: `sagas: concurrent writer: execution "a" seq 2`,
			want:    map[string][]Event{"a": {e1, e2}},
		},
		{
			name:    "[Fail] When: another writer appends Seq 1 of a taken execution ; Should: ErrExecutionExists",
			prior:   []Event{e1},
			event:   withEvent(&e1, func(e *Event) { e.Writer = "w2" }),
			wantErr: ErrExecutionExists,
			wantMsg: `sagas: execution exists: execution "a" seq 1`,
			want:    map[string][]Event{"a": {e1}},
		},
		{
			name:    "[Fail] When: another writer appends a taken later Seq ; Should: return ErrConcurrentWriter",
			prior:   []Event{e1, e2},
			event:   withEvent(&e2, func(e *Event) { e.Writer = "w2" }),
			wantErr: ErrConcurrentWriter,
			wantMsg: `sagas: concurrent writer: execution "a" seq 2`,
			want:    map[string][]Event{"a": {e1, e2}},
		},
		{
			name:  "[Success] When: Seq 1 of another execution ; Should: write it and leave the first one unchanged",
			prior: []Event{e1, e2},
			event: &b1,
			want:  map[string][]Event{"a": {e1, e2}, "b": {b1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			j := newMemoryJournal()
			for i := range tt.prior {
				require.NoError(t, j.Append(t.Context(), &tt.prior[i]))
			}

			err := j.Append(t.Context(), tt.event)

			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantMsg, errText(err))

			for execution, want := range tt.want {
				scribble(j.events(execution))
				assert.Equal(t, want, j.events(execution), execution)
			}

			assert.Equal(t, leak{}, callerChangeLeaks(t, j))
		})
	}
}

func Test_sameEvent(t *testing.T) {
	t.Parallel()

	base := journalEvent("a", 1)
	now := time.Now()

	tests := []struct {
		name string
		a    *Event
		b    *Event
		want bool
	}{
		{name: "[Success] When: the same event ; Should: return true", a: &base, b: &base, want: true},
		{
			name: "[Success] When: equal fields in new slices ; Should: return true",
			a:    &base, b: withEvent(&base, func(*Event) {}), want: true,
		},
		{
			name: "[Success] When: Time differs only in its monotonic reading ; Should: return true",
			a:    withEvent(&base, func(e *Event) { e.Time = now }),
			b:    withEvent(&base, func(e *Event) { e.Time = now.Round(0) }),
			want: true,
		},
		{
			name: "[Success] When: the same instant in another location ; Should: return true",
			a:    &base,
			b:    withEvent(&base, func(e *Event) { e.Time = e.Time.In(time.FixedZone("x", 3600)) }),
			want: true,
		},
		{
			name: "[Success] When: nil slices against empty slices ; Should: return true, nil equals empty",
			a:    withEvent(&base, func(e *Event) { e.Changed, e.Steps = nil, nil }),
			b:    withEvent(&base, func(e *Event) { e.Changed, e.Steps = []string{}, []StepZone{} }),
			want: true,
		},
		{
			name: "[Fail] When: Saga differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Saga = "other" }),
		},
		{
			name: "[Fail] When: Execution differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Execution = "other" }),
		},
		{
			name: "[Fail] When: Writer differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Writer = "w2" }),
		},
		{
			name: "[Fail] When: Seq differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Seq = 2 }),
		},
		{
			name: "[Fail] When: Kind differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Kind = EventStepFailed }),
		},
		{
			name: "[Fail] When: Step differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Step = "charge" }),
		},
		{
			name: "[Fail] When: Attempt differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Attempt = 2 }),
		},
		{
			name: "[Fail] When: Err differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Err = "boom" }),
		},
		{
			name: "[Fail] When: a Changed name differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Changed[0] = "Total" }),
		},
		{
			name: "[Fail] When: Changed has one more name ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Changed = append(e.Changed, "Total") }),
		},
		{
			name: "[Fail] When: a Steps name differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Steps[0].Name = "charge" }),
		},
		{
			name: "[Fail] When: a Steps zone differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Steps[0].Zone = ZonePivot }),
		},
		{
			name: "[Fail] When: Status differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Status = StatusCompleted }),
		},
		{
			name: "[Fail] When: Time differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Time = e.Time.Add(time.Nanosecond) }),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, sameEvent(tt.a, tt.b))
		})
	}
}

func Test_memoryJournal_Append_concurrent(t *testing.T) {
	t.Parallel()

	const seqs = 20

	tests := []struct {
		name          string
		executions    int
		writersPerSeq int
	}{
		{
			name:       "[Success] When: many executions append in parallel ; Should: end each with Seq 1 to n",
			executions: 32, writersPerSeq: 1,
		},
		{
			name:       "[Success] When: many writers race for each Seq of one execution ; Should: accept one per Seq",
			executions: 1, writersPerSeq: 8,
		},
		{
			name:       "[Success] When: many writers race in many executions ; Should: accept one per Seq each",
			executions: 8, writersPerSeq: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			j := newMemoryJournal()
			results := make([][][]error, tt.executions)

			var wg sync.WaitGroup

			for x := range tt.executions {
				results[x] = make([][]error, seqs)

				wg.Go(func() { raceExecution(t, j, strconv.Itoa(x), results[x], tt.writersPerSeq) })
			}

			wg.Wait()

			for x := range tt.executions {
				assertRaceOutcome(t, j.events(strconv.Itoa(x)), results[x])
			}
		})
	}
}

func raceExecution(t *testing.T, j *memoryJournal, execution string, results [][]error, writers int) {
	t.Helper()

	for i := range results {
		results[i] = make([]error, writers)

		var wg sync.WaitGroup
		for w := range writers {
			wg.Go(func() {
				e := journalEvent(execution, i+1)
				e.Writer = "w" + strconv.Itoa(w)
				results[i][w] = j.Append(t.Context(), &e)
			})
		}

		wg.Wait()
	}
}

func assertRaceOutcome(t *testing.T, events []Event, results [][]error) {
	t.Helper()

	require.Len(t, events, len(results))

	for i, errs := range results {
		assert.Equal(t, i+1, events[i].Seq)

		rejection := ErrConcurrentWriter
		if i == 0 {
			rejection = ErrExecutionExists
		}

		accepted := 0

		for _, err := range errs {
			if err == nil {
				accepted++

				continue
			}

			require.ErrorIs(t, err, rejection)
		}

		assert.Equal(t, 1, accepted, "seq %d", i+1)
	}
}

func Benchmark_memoryJournal_Append(b *testing.B) {
	const streamLen = 64

	j := newMemoryJournal()
	e := journalEvent("", 0)
	n := 0

	for b.Loop() {
		e.Seq = n%streamLen + 1
		if e.Seq == 1 {
			e.Execution = strconv.Itoa(n / streamLen)
		}

		n++

		if err := j.Append(b.Context(), &e); err != nil {
			b.Fatal(err)
		}
	}
}
