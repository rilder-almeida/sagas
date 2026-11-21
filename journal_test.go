package sagas

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func journalEvent(execution string, seq int) Event {
	return Event{
		Saga:      "booking",
		Execution: execution,
		Writer:    "w1",
		Seq:       seq,
		Kind:      EventStepSucceeded,
		Step:      "reserve",
		Attempt:   1,
		Err:       EventError{Kind: nameFailed, Message: "declined", Cause: "stopped", Previous: []string{"first"}},
		Changed:   []string{"Seats"},
		Steps:     []StepZone{{Name: "reserve", Zone: ZoneCompensable}},
		Status:    statusUnfinished,
		Time:      time.Date(2026, 11, 17, 10, 0, seq, 0, time.UTC),
	}
}

func withEvent(e *Event, change func(*Event)) *Event {
	out := *e
	out.Changed = slices.Clone(out.Changed)
	out.Steps = slices.Clone(out.Steps)
	out.Err.Previous = slices.Clone(out.Err.Previous)
	change(&out)

	return &out
}

const scribbled = "scribbled"

func scribble(events []Event) {
	for i := range events {
		scribbleEvent(&events[i])
	}
}

func scribbleEvent(e *Event) {
	if e == nil {
		return
	}

	e.Step = scribbled
	e.Seq = 99

	for k := range e.Changed {
		e.Changed[k] = scribbled
	}

	for k := range e.Steps {
		e.Steps[k].Name = scribbled
	}

	for k := range e.Err.Previous {
		e.Err.Previous[k] = scribbled
	}
}

func prime(t *testing.T, j *MemoryJournal, prior []Event) {
	t.Helper()

	for i := range prior {
		require.NoError(t, j.Append(t.Context(), &prior[i]))
	}
}

func touchedBy(prior []Event, event *Event) []string {
	touched := make([]string, 0, len(prior)+1)
	for i := range prior {
		touched = append(touched, prior[i].Execution)
	}

	if event != nil {
		touched = append(touched, event.Execution)
	}

	return touched
}

func Test_MemoryJournal_Append(t *testing.T) {
	t.Parallel()

	now := time.Now()
	noSlices := func(e *Event) { e.Changed, e.Steps = nil, nil }
	stamp := func(e *Event) { e.Time = now }
	stamped := func() Event { return *withEvent(new(journalEvent("a", 1)), stamp) }

	tests := []struct {
		name     string
		zero     bool
		canceled bool
		prior    []Event
		event    *Event
		wantErr  error
		wantMsg  string
		want     map[string][]Event
	}{
		{
			name:  "[Success] When: the first event of a new execution ; Should: write it",
			event: new(journalEvent("a", 1)),
			want:  map[string][]Event{"a": {journalEvent("a", 1)}},
		},
		{
			name:  "[Success] When: the journal is a zero value ; Should: write the first event",
			zero:  true,
			event: new(journalEvent("a", 1)),
			want:  map[string][]Event{"a": {journalEvent("a", 1)}},
		},
		{
			name:  "[Success] When: the next Seq ; Should: write it after the last event",
			prior: []Event{journalEvent("a", 1)},
			event: new(journalEvent("a", 2)),
			want:  map[string][]Event{"a": {journalEvent("a", 1), journalEvent("a", 2)}},
		},
		{
			name:  "[Success] When: the first event has nil Changed and Steps ; Should: store them as nil",
			event: withEvent(new(journalEvent("c", 1)), noSlices),
			want:  map[string][]Event{"c": {*withEvent(new(journalEvent("c", 1)), noSlices)}},
		},
		{
			name:    "[Fail] When: Seq 5 after Seq 1 to 3 ; Should: return ErrConcurrentWriter and write nothing",
			prior:   []Event{journalEvent("a", 1), journalEvent("a", 2), journalEvent("a", 3)},
			event:   new(journalEvent("a", 5)),
			wantErr: ErrConcurrentWriter,
			wantMsg: `sagas: concurrent writer: execution "a" seq 5`,
			want:    map[string][]Event{"a": {journalEvent("a", 1), journalEvent("a", 2), journalEvent("a", 3)}},
		},
		{
			name:    "[Fail] When: Seq 0 on an empty execution ; Should: return errInvalidEvent and write nothing",
			event:   new(journalEvent("a", 0)),
			wantErr: errInvalidEvent,
			wantMsg: `sagas: invalid event: execution "a" seq 0`,
			want:    map[string][]Event{"a": {}},
		},
		{
			name:    "[Fail] When: Seq -1 after Seq 1 ; Should: return errInvalidEvent and write nothing",
			prior:   []Event{journalEvent("a", 1)},
			event:   new(journalEvent("a", -1)),
			wantErr: errInvalidEvent,
			wantMsg: `sagas: invalid event: execution "a" seq -1`,
			want:    map[string][]Event{"a": {journalEvent("a", 1)}},
		},
		{
			name:    "[Fail] When: a nil event after Seq 1 ; Should: return errInvalidEvent and write nothing",
			prior:   []Event{journalEvent("a", 1)},
			wantErr: errInvalidEvent,
			wantMsg: `sagas: invalid event: nil event`,
			want:    map[string][]Event{"a": {journalEvent("a", 1)}},
		},
		{
			name:  "[Success] When: an equal resend, new slices, monotonic-only Time ; Should: nil, no write",
			prior: []Event{stamped()},
			event: withEvent(new(journalEvent("a", 1)), func(e *Event) { e.Time = now.Round(0) }),
			want:  map[string][]Event{"a": {stamped()}},
		},
		{
			name:  "[Success] When: an equal resend of an earlier Seq ; Should: return nil, write nothing",
			prior: []Event{journalEvent("a", 1), journalEvent("a", 2), journalEvent("a", 3)},
			event: new(journalEvent("a", 2)),
			want:  map[string][]Event{"a": {journalEvent("a", 1), journalEvent("a", 2), journalEvent("a", 3)}},
		},
		{
			name:    "[Fail] When: the same Seq and Writer with another Kind ; Should: return ErrConcurrentWriter",
			prior:   []Event{journalEvent("a", 1), journalEvent("a", 2)},
			event:   withEvent(new(journalEvent("a", 2)), func(e *Event) { e.Kind = EventStepFailed }),
			wantErr: ErrConcurrentWriter,
			wantMsg: `sagas: concurrent writer: execution "a" seq 2`,
			want:    map[string][]Event{"a": {journalEvent("a", 1), journalEvent("a", 2)}},
		},
		{
			name:    "[Fail] When: another writer appends Seq 1 of a taken execution ; Should: ErrExecutionExists",
			prior:   []Event{journalEvent("a", 1)},
			event:   withEvent(new(journalEvent("a", 1)), func(e *Event) { e.Writer = "w2" }),
			wantErr: ErrExecutionExists,
			wantMsg: `sagas: execution exists: execution "a" seq 1`,
			want:    map[string][]Event{"a": {journalEvent("a", 1)}},
		},
		{
			name:    "[Fail] When: another writer appends a taken later Seq ; Should: return ErrConcurrentWriter",
			prior:   []Event{journalEvent("a", 1), journalEvent("a", 2)},
			event:   withEvent(new(journalEvent("a", 2)), func(e *Event) { e.Writer = "w2" }),
			wantErr: ErrConcurrentWriter,
			wantMsg: `sagas: concurrent writer: execution "a" seq 2`,
			want:    map[string][]Event{"a": {journalEvent("a", 1), journalEvent("a", 2)}},
		},
		{
			name:     "[Fail] When: the next Seq with a done context ; Should: return the context error, write nothing",
			canceled: true,
			prior:    []Event{journalEvent("a", 1)},
			event:    new(journalEvent("a", 2)),
			wantErr:  context.Canceled,
			wantMsg:  "context canceled",
			want:     map[string][]Event{"a": {journalEvent("a", 1)}},
		},
		{
			name:  "[Success] When: Seq 1 of another execution ; Should: write it and leave the first one unchanged",
			prior: []Event{journalEvent("a", 1), journalEvent("a", 2)},
			event: new(journalEvent("b", 1)),
			want: map[string][]Event{
				"a": {journalEvent("a", 1), journalEvent("a", 2)},
				"b": {journalEvent("b", 1)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			j := NewMemoryJournal()
			if tt.zero {
				j = &MemoryJournal{}
			}

			prime(t, j, tt.prior)
			touched := touchedBy(tt.prior, tt.event)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			if tt.canceled {
				cancel()
			}

			err := j.Append(ctx, tt.event)
			scribbleEvent(tt.event)

			slices.Sort(touched)
			assert.Equal(t, slices.Compact(touched), slices.Sorted(maps.Keys(tt.want)))

			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantMsg, errText(err))

			for execution, want := range tt.want {
				scribble(j.Events(execution))
				assert.Equal(t, want, j.Events(execution), execution)
			}
		})
	}
}

func Test_NewMemoryJournal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		seed       []Event
		readSecond bool
		want       []Event
	}{
		{
			name: "[Success] When: nothing was appended ; Should: hold no events",
			want: []Event{},
		},
		{
			name: "[Success] When: an event went to the first journal ; Should: hold it there",
			seed: []Event{journalEvent("a", 1)},
			want: []Event{journalEvent("a", 1)},
		},
		{
			name:       "[Success] When: an event went to the first journal ; Should: leave a second journal empty",
			seed:       []Event{journalEvent("a", 1)},
			readSecond: true,
			want:       []Event{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			first, second := NewMemoryJournal(), NewMemoryJournal()
			for i := range tt.seed {
				require.NoError(t, first.Append(t.Context(), &tt.seed[i]))
			}

			read := first
			if tt.readSecond {
				read = second
			}

			assert.Equal(t, tt.want, read.Events("a"))
		})
	}
}

func Test_MemoryJournal_Events(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stored    []Event
		execution string
		scribble  bool
		want      []Event
	}{
		{
			name:      "[Success] When: the execution has events ; Should: return them in Seq order",
			stored:    []Event{journalEvent("a", 1), journalEvent("a", 2)},
			execution: "a",
			want:      []Event{journalEvent("a", 1), journalEvent("a", 2)},
		},
		{
			name:      "[Success] When: the execution is unknown ; Should: return no events",
			stored:    []Event{journalEvent("a", 1)},
			execution: "b",
			want:      []Event{},
		},
		{
			name:      "[Success] When: the caller changes a returned event ; Should: return the stored ones",
			stored:    []Event{journalEvent("a", 1), journalEvent("a", 2)},
			execution: "a",
			scribble:  true,
			want:      []Event{journalEvent("a", 1), journalEvent("a", 2)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			j := NewMemoryJournal()
			for i := range tt.stored {
				require.NoError(t, j.Append(t.Context(), &tt.stored[i]))
			}

			if tt.scribble {
				scribble(j.Events(tt.execution))
			}

			assert.Equal(t, tt.want, j.Events(tt.execution))
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
			name: "[Fail] When: the Err kind differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Err.Kind = nameUnknown }),
		},
		{
			name: "[Fail] When: the Err message differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Err.Message = "boom" }),
		},
		{
			name: "[Fail] When: the Err cause differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Err.Cause = "other" }),
		},
		{
			name: "[Fail] When: an Err previous text differs ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Err.Previous[0] = "second" }),
		},
		{
			name: "[Fail] When: Err has one more previous text ; Should: return false",
			a:    &base, b: withEvent(&base, func(e *Event) { e.Err.Previous = append(e.Err.Previous, "second") }),
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

func Test_MemoryJournal_Append_concurrent(t *testing.T) {
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

			j := NewMemoryJournal()
			results := make([][][]error, tt.executions)

			var wg sync.WaitGroup

			for x := range tt.executions {
				results[x] = make([][]error, seqs)

				wg.Go(func() { raceExecution(t, j, strconv.Itoa(x), results[x], tt.writersPerSeq) })
			}

			wg.Wait()

			for x := range tt.executions {
				assertRaceOutcome(t, j.Events(strconv.Itoa(x)), results[x])
			}
		})
	}
}

func raceExecution(t *testing.T, j *MemoryJournal, execution string, results [][]error, writers int) {
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

func Benchmark_MemoryJournal_Append(b *testing.B) {
	const streamLen = 64

	j := NewMemoryJournal()
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
