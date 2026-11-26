package sagastest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type keepsPointer struct {
	*sagas.MemoryJournal

	mu   sync.Mutex
	kept map[string]*sagas.Event
}

func (j *keepsPointer) Append(ctx context.Context, e *sagas.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	key := fmt.Sprintf("%s/%d", e.Execution, e.Seq)
	if j.kept[key] == e {
		return nil
	}

	err := j.MemoryJournal.Append(ctx, e)
	if err == nil {
		j.kept[key] = e
	}

	return err
}

type keepsValueBytes struct {
	*sagas.MemoryJournal

	mu   sync.Mutex
	kept map[string]sagas.Event
}

func (j *keepsValueBytes) Append(ctx context.Context, e *sagas.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	key := fmt.Sprintf("%s/%d", e.Execution, e.Seq)
	if prev, ok := j.kept[key]; ok && reflect.DeepEqual(prev, *e) {
		return nil
	}

	err := j.MemoryJournal.Append(ctx, e)
	if err == nil {
		kept := *e
		kept.Values = slices.Clone(e.Values)
		kept.Steps = slices.Clone(e.Steps)
		kept.Err.Previous = slices.Clone(e.Err.Previous)
		j.kept[key] = kept
	}

	return err
}

type keepsSlices struct {
	*sagas.MemoryJournal

	mu   sync.Mutex
	kept map[string]sagas.Event
}

func (j *keepsSlices) Append(ctx context.Context, e *sagas.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	key := fmt.Sprintf("%s/%d", e.Execution, e.Seq)
	if prev, ok := j.kept[key]; ok && reflect.DeepEqual(prev, *e) {
		return nil
	}

	err := j.MemoryJournal.Append(ctx, e)
	if err == nil {
		j.kept[key] = *e
	}

	return err
}

func storedOf(ctx context.Context, j *sagas.MemoryJournal, execution string) ([]sagas.Event, error) {
	events, err := j.Events(ctx, execution)
	if errors.Is(err, sagas.ErrNoEvents) {
		return nil, nil
	}

	return events, err
}

type journalAcceptsGaps struct{ *sagas.MemoryJournal }

func (j journalAcceptsGaps) Append(ctx context.Context, e *sagas.Event) error {
	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored, err := storedOf(ctx, j.MemoryJournal, e.Execution)
	if err != nil {
		return err
	}

	if e.Seq > len(stored)+1 {
		return nil
	}

	return j.MemoryJournal.Append(ctx, e)
}

type acceptsLowSeq struct{ *sagas.MemoryJournal }

func (j acceptsLowSeq) Append(ctx context.Context, e *sagas.Event) error {
	if e != nil && e.Seq < 1 {
		return nil
	}

	return j.MemoryJournal.Append(ctx, e)
}

type rejectsEqualResend struct{ *sagas.MemoryJournal }

func (j rejectsEqualResend) Append(ctx context.Context, e *sagas.Event) error {
	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored, err := storedOf(ctx, j.MemoryJournal, e.Execution)
	if err != nil {
		return err
	}

	if e.Seq == 1 && len(stored) > 0 {
		return fmt.Errorf("%w: resend", sagas.ErrExecutionExists)
	}

	if e.Seq > 1 && e.Seq <= len(stored) {
		return fmt.Errorf("%w: resend", sagas.ErrConcurrentWriter)
	}

	return j.MemoryJournal.Append(ctx, e)
}

type dropsCompensable struct{ *sagas.MemoryJournal }

func (j dropsCompensable) Append(ctx context.Context, e *sagas.Event) error {
	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored := *e
	stored.Steps = slices.Clone(e.Steps)

	for i := range stored.Steps {
		stored.Steps[i].Compensable = false
	}

	return j.MemoryJournal.Append(ctx, &stored)
}

type ignoresVersion struct{ *sagas.MemoryJournal }

func (j ignoresVersion) Append(ctx context.Context, e *sagas.Event) error {
	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored := *e
	stored.Version = sagas.EventVersion

	return j.MemoryJournal.Append(ctx, &stored)
}

type zeroesVersion struct{ *sagas.MemoryJournal }

func (j zeroesVersion) Events(ctx context.Context, execution string) ([]sagas.Event, error) {
	events, err := j.MemoryJournal.Events(ctx, execution)
	for i := range events {
		events[i].Version = ""
	}

	return events, err
}

type dropsCause struct{ *sagas.MemoryJournal }

func (j dropsCause) Events(ctx context.Context, execution string) ([]sagas.Event, error) {
	events, err := j.MemoryJournal.Events(ctx, execution)
	for i := range events {
		events[i].Err.Cause = ""
	}

	return events, err
}

type dropsStatus struct{ *sagas.MemoryJournal }

func (j dropsStatus) Events(ctx context.Context, execution string) ([]sagas.Event, error) {
	events, err := j.MemoryJournal.Events(ctx, execution)
	for i := range events {
		events[i].Status = ""
	}

	return events, err
}

type readsEmptyAsNil struct{ *sagas.MemoryJournal }

func (j readsEmptyAsNil) Events(ctx context.Context, execution string) ([]sagas.Event, error) {
	events, err := j.MemoryJournal.Events(ctx, execution)
	for i := range events {
		if len(events[i].Values) == 0 {
			events[i].Values = nil
		}

		if len(events[i].Steps) == 0 {
			events[i].Steps = nil
		}

		if len(events[i].Err.Previous) == 0 {
			events[i].Err.Previous = nil
		}
	}

	return events, err
}

type readIgnoresContext struct{ *sagas.MemoryJournal }

func (j readIgnoresContext) Events(_ context.Context, execution string) ([]sagas.Event, error) {
	return j.MemoryJournal.Events(context.Background(), execution)
}

type emptyForMissing struct{ *sagas.MemoryJournal }

func (j emptyForMissing) Events(ctx context.Context, execution string) ([]sagas.Event, error) {
	events, err := j.MemoryJournal.Events(ctx, execution)
	if errors.Is(err, sagas.ErrNoEvents) {
		return []sagas.Event{}, nil
	}

	return events, err
}

type appendOnly struct{ journal sagas.Journal }

func (j appendOnly) Append(ctx context.Context, e *sagas.Event) error {
	return j.journal.Append(ctx, e)
}

type acceptsOtherEvent struct {
	*sagas.MemoryJournal

	mu sync.Mutex
}

func (j *acceptsOtherEvent) Append(ctx context.Context, e *sagas.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored, err := storedOf(ctx, j.MemoryJournal, e.Execution)
	if err != nil {
		return err
	}

	if e.Seq >= 1 && e.Seq <= len(stored) {
		return nil
	}

	return j.MemoryJournal.Append(ctx, e)
}

type swapsSentinel struct {
	*sagas.MemoryJournal

	from, to error
}

func (j swapsSentinel) Append(ctx context.Context, e *sagas.Event) error {
	err := j.MemoryJournal.Append(ctx, e)
	if errors.Is(err, j.from) {
		return fmt.Errorf("%w: swapped", j.to)
	}

	return err
}

type acceptsSecondWriter struct {
	*sagas.MemoryJournal

	mu      sync.Mutex
	fromSeq int
}

func (j *acceptsSecondWriter) Append(ctx context.Context, e *sagas.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored, err := storedOf(ctx, j.MemoryJournal, e.Execution)
	if err != nil {
		return err
	}

	if e.Seq >= j.fromSeq && e.Seq <= len(stored) && stored[e.Seq-1].Writer != e.Writer {
		return nil
	}

	return j.MemoryJournal.Append(ctx, e)
}

type acceptsGapOnEmpty struct{ *sagas.MemoryJournal }

func (j acceptsGapOnEmpty) Append(ctx context.Context, e *sagas.Event) error {
	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored, err := storedOf(ctx, j.MemoryJournal, e.Execution)
	if err != nil {
		return err
	}

	if e.Seq > 1 && len(stored) == 0 {
		return nil
	}

	return j.MemoryJournal.Append(ctx, e)
}

type firstWriteUnchecked struct {
	*sagas.MemoryJournal

	mu     sync.Mutex
	stored bool
}

func (j *firstWriteUnchecked) Append(ctx context.Context, e *sagas.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if e != nil && e.Seq > 1 && !j.stored {
		return nil
	}

	err := j.MemoryJournal.Append(ctx, e)
	if err == nil {
		j.stored = true
	}

	return err
}

type swapsSentinelForSecondWriter struct {
	*sagas.MemoryJournal

	mu sync.Mutex
}

func (j *swapsSentinelForSecondWriter) Append(ctx context.Context, e *sagas.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	err := j.MemoryJournal.Append(ctx, e)
	if e == nil || !errors.Is(err, sagas.ErrConcurrentWriter) {
		return err
	}

	stored, readErr := storedOf(ctx, j.MemoryJournal, e.Execution)
	if readErr != nil {
		return errors.Join(err, readErr)
	}

	if e.Seq <= len(stored) && stored[e.Seq-1].Writer != e.Writer {
		return fmt.Errorf("%w: swapped", sagas.ErrExecutionExists)
	}

	return err
}

type ignoresContext struct{ *sagas.MemoryJournal }

func (j ignoresContext) Append(_ context.Context, e *sagas.Event) error {
	return j.MemoryJournal.Append(context.Background(), e)
}

type storesThenFails struct{ *sagas.MemoryJournal }

func (j storesThenFails) Append(ctx context.Context, e *sagas.Event) error {
	if err := j.MemoryJournal.Append(context.Background(), e); err != nil {
		return err
	}

	return ctx.Err()
}

type acceptsNil struct{ *sagas.MemoryJournal }

func (j acceptsNil) Append(ctx context.Context, e *sagas.Event) error {
	if e == nil {
		return nil
	}

	return j.MemoryJournal.Append(ctx, e)
}

type comparesTimeWithEquals struct{ *sagas.MemoryJournal }

func (j comparesTimeWithEquals) Append(ctx context.Context, e *sagas.Event) error {
	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored, err := storedOf(ctx, j.MemoryJournal, e.Execution)
	if err != nil {
		return err
	}

	if e.Seq >= 1 && e.Seq <= len(stored) && stored[e.Seq-1].Time != e.Time {
		return fmt.Errorf("%w: time differs", sagas.ErrConcurrentWriter)
	}

	return j.MemoryJournal.Append(ctx, e)
}

type rejectsSamePointer struct {
	*sagas.MemoryJournal

	mu   sync.Mutex
	seen map[*sagas.Event]bool
}

func (j *rejectsSamePointer) Append(ctx context.Context, e *sagas.Event) error {
	j.mu.Lock()
	seen := j.seen[e]
	j.seen[e] = true
	j.mu.Unlock()

	if e != nil && seen {
		return fmt.Errorf("%w: same pointer", sagas.ErrConcurrentWriter)
	}

	return j.MemoryJournal.Append(ctx, e)
}

type journalRow struct {
	name         string
	newJournal   func(*testing.T) sagas.Journal
	wantProbeErr bool
	wantFailing  []string
	wantCauses   []string
	wantWrapped  []error
}

func journalRows() []journalRow {
	return []journalRow{
		{
			name:       "[Success] When: the journal is the memory journal ; Should: report nothing",
			newJournal: func(*testing.T) sagas.Journal { return sagas.NewMemoryJournal() },
		},
		{
			name: "[Fail] When: the probe holds an event ; Should: refuse the probe before any case",
			newJournal: func(t *testing.T) sagas.Journal {
				t.Helper()

				return holdingEvent(t)
			},
			wantProbeErr: true,
			wantCauses:   []string{`Events of execution "execution" returned 1 events, want 0`},
		},
		{
			name: "[Fail] When: the journal keeps the caller pointer ; Should: report an accepted changed resend",
			newJournal: func(*testing.T) sagas.Journal {
				return &keepsPointer{MemoryJournal: sagas.NewMemoryJournal(), kept: map[string]*sagas.Event{}}
			},
			wantFailing: []string{ruleStoredCopy},
			wantCauses: []string{
				"resend of seq 1 after the caller changed its scalar field returned nil",
				"resend of seq 1 after the caller changed its Values element returned nil",
			},
		},
		{
			name: "[Fail] When: the journal keeps the caller slices ; Should: report an accepted changed resend",
			newJournal: func(*testing.T) sagas.Journal {
				return &keepsSlices{MemoryJournal: sagas.NewMemoryJournal(), kept: map[string]sagas.Event{}}
			},
			wantFailing: []string{ruleStoredCopy},
			wantCauses: []string{
				"resend of seq 1 after the caller changed its Steps slice returned nil",
				"resend of seq 1 after the caller changed its Err.Previous slice returned nil",
			},
		},
		{
			name: "[Fail] When: the journal copies Values but keeps the bytes of each Value ; " +
				"Should: report the accepted changed byte",
			newJournal: func(*testing.T) sagas.Journal {
				return &keepsValueBytes{MemoryJournal: sagas.NewMemoryJournal(), kept: map[string]sagas.Event{}}
			},
			wantFailing: []string{ruleStoredCopy},
			wantCauses:  []string{"resend of seq 1 after the caller changed its Values byte returned nil"},
		},
		{
			name:        "[Fail] When: the journal drops Compensable of each step ; Should: report the accepted change",
			newJournal:  func(*testing.T) sagas.Journal { return dropsCompensable{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleStoredCopy, ruleStoredEvents},
			wantCauses: []string{
				`Events of execution "execution" returned event 1 as`,
				"resend of seq 1 after the caller changed its Steps Compensable from true returned nil",
				"resend of seq 1 after the caller changed its Steps Compensable from false returned nil",
			},
		},
		{
			name:        "[Fail] When: the journal ignores Version ; Should: report the unknown versions it accepted",
			newJournal:  func(*testing.T) sagas.Journal { return ignoresVersion{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleUnknownVersion, ruleStoredCopy},
			wantCauses: []string{
				`Append of an event with version "v0.1.0" returned nil`,
				`Append of an event with version "v9.0.0" returned nil`,
				"resend of seq 1 after the caller changed its Version returned nil",
			},
		},
		{
			name:        "[Fail] When: the journal stores Version empty ; Should: report the stored events that differ",
			newJournal:  func(*testing.T) sagas.Journal { return zeroesVersion{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleStoredEvents, ruleStoredCopy},
			wantCauses:  []string{"Version: Kind:", `Events of execution "execution" returned event 1 as`},
		},
		{
			name:        "[Fail] When: the journal drops Err.Cause ; Should: report the stored events that differ",
			newJournal:  func(*testing.T) sagas.Journal { return dropsCause{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleStoredEvents, ruleStoredCopy},
			wantCauses:  []string{"Cause: Previous", `Events of execution "execution" returned event 2 as`},
		},
		{
			name:        "[Fail] When: the journal drops Status ; Should: report the stored events that differ",
			newJournal:  func(*testing.T) sagas.Journal { return dropsStatus{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleStoredEvents, ruleStoredCopy},
			wantCauses:  []string{`Events of execution "execution" returned event 1 as`, "Status: Time"},
		},
		{
			name:       "[Success] When: the journal reads an empty slice back as nil ; Should: report nothing",
			newJournal: func(*testing.T) sagas.Journal { return readsEmptyAsNil{sagas.NewMemoryJournal()} },
		},
		{
			name:        "[Fail] When: Events ignores a done context ; Should: report the events it returned",
			newJournal:  func(*testing.T) sagas.Journal { return readIgnoresContext{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleDoneContext},
			wantCauses:  []string{`Events of execution "execution" with a done context returned nil`},
		},
		{
			name:        "[Fail] When: the journal returns an empty slice for a missing execution ; Should: report it",
			newJournal:  func(*testing.T) sagas.Journal { return emptyForMissing{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleNoEvents},
			wantCauses:  []string{`Events of execution "missing" returned 0 events and a nil error`},
		},
		{
			name:         "[Fail] When: the journal has no Events method ; Should: refuse the probe as no EventReader",
			newJournal:   func(*testing.T) sagas.Journal { return appendOnly{sagas.NewMemoryJournal()} },
			wantProbeErr: true,
			wantCauses:   []string{"does not implement sagas.EventReader"},
		},
		{
			name:        "[Fail] When: the journal accepts a gap ; Should: report that seq 3 after seq 1 was accepted",
			newJournal:  func(*testing.T) sagas.Journal { return journalAcceptsGaps{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleGap},
			wantCauses:  []string{"Append of seq 3 after seq 1 returned nil"},
		},
		{
			name:        "[Fail] When: the journal accepts seq 2 on an empty journal ; Should: report seq 2",
			newJournal:  func(*testing.T) sagas.Journal { return acceptsGapOnEmpty{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleGap},
			wantCauses:  []string{"Append of seq 2 on an empty execution returned nil"},
		},
		{
			name: "[Fail] When: the journal skips the Seq check on its first write ; " +
				"Should: report seq 2 on an empty journal",
			newJournal: func(*testing.T) sagas.Journal {
				return &firstWriteUnchecked{MemoryJournal: sagas.NewMemoryJournal()}
			},
			wantFailing: []string{ruleGap},
			wantCauses:  []string{"Append of seq 2 on an empty journal returned nil"},
		},
		{
			name:        "[Fail] When: the journal accepts a Seq below 1 ; Should: report the accepted seq 0 and -1",
			newJournal:  func(*testing.T) sagas.Journal { return acceptsLowSeq{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleSeqBelowOne, ruleInvalidEvent},
			wantCauses: []string{
				"Append of seq 0 returned nil", "Append of seq -1 returned nil",
				"Append of an event with seq 0 returned nil",
			},
		},
		{
			name:        "[Fail] When: the journal rejects an equal resend ; Should: report the rejected equal copy",
			newJournal:  func(*testing.T) sagas.Journal { return rejectsEqualResend{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleEqualResend, ruleStoredCopy, ruleTimeZone, ruleExactResend},
			wantCauses: []string{
				"Append of an equal copy of seq 1 returned",
				"resend of an unchanged copy of seq 1 after the caller changed its scalar field returned",
				"Append of an equal instant in another zone returned",
				"Append of the same event again returned",
			},
		},
		{
			name: "[Fail] When: the journal accepts another event at a taken Seq ; Should: report it",
			newJournal: func(*testing.T) sagas.Journal {
				return &acceptsOtherEvent{MemoryJournal: sagas.NewMemoryJournal()}
			},
			wantFailing: []string{ruleDifferentEvent, ruleStoredCopy, ruleConcurrent},
			wantCauses: []string{
				"Append of another event at taken seq 1 returned nil",
				"after the caller changed its scalar field returned nil",
				"returned nil, want 1",
			},
		},
		{
			name: "[Fail] When: the journal returns ErrConcurrentWriter at Seq 1 ; Should: report the wrong sentinel",
			newJournal: func(*testing.T) sagas.Journal {
				return swapsSentinel{sagas.NewMemoryJournal(), sagas.ErrExecutionExists, sagas.ErrConcurrentWriter}
			},
			wantFailing: []string{ruleRejectionFirst, ruleConcurrent},
			wantCauses: []string{
				"Append of another event at taken seq 1 returned",
				"want an error wrapping " + sagas.ErrExecutionExists.Error(),
				"a losing append of seq 1 returned",
			},
			wantWrapped: []error{sagas.ErrConcurrentWriter},
		},
		{
			name: "[Fail] When: the journal returns ErrExecutionExists later ; Should: report the wrong sentinel",
			newJournal: func(*testing.T) sagas.Journal {
				return swapsSentinel{sagas.NewMemoryJournal(), sagas.ErrConcurrentWriter, sagas.ErrExecutionExists}
			},
			wantFailing: []string{ruleRejectionLater, ruleConcurrent},
			wantCauses: []string{
				"Append of another event at taken seq 2 returned", "a losing append of seq 3 returned",
				"want an error wrapping " + sagas.ErrConcurrentWriter.Error(),
			},
		},
		{
			name: "[Fail] When: the journal accepts a second writer at a taken Seq ; Should: report two winners",
			newJournal: func(*testing.T) sagas.Journal {
				return &acceptsSecondWriter{MemoryJournal: sagas.NewMemoryJournal(), fromSeq: 1}
			},
			wantFailing: []string{ruleConcurrent},
			wantCauses:  []string{"returned nil, want 1"},
		},
		{
			name:        "[Fail] When: the journal ignores a done context ; Should: report the nil error",
			newJournal:  func(*testing.T) sagas.Journal { return ignoresContext{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleDoneContext},
			wantCauses:  []string{"with a done context returned nil", "after a done context returned"},
		},
		{
			name:        "[Fail] When: the journal stores then fails with ctx ; Should: report the rejected event",
			newJournal:  func(*testing.T) sagas.Journal { return storesThenFails{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleDoneContext},
			wantCauses:  []string{"after a done context returned"},
		},
		{
			name:        "[Fail] When: the journal accepts a nil event ; Should: report the nil event",
			newJournal:  func(*testing.T) sagas.Journal { return acceptsNil{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleInvalidEvent},
			wantCauses:  []string{"Append of a nil event returned nil"},
		},
		{
			name:        "[Fail] When: the journal compares Time with == ; Should: report the rejected equal instant",
			newJournal:  func(*testing.T) sagas.Journal { return comparesTimeWithEquals{sagas.NewMemoryJournal()} },
			wantFailing: []string{ruleTimeZone},
			wantCauses:  []string{"in another zone returned"},
		},
		{
			name: "[Fail] When: the journal rejects the exact resend ; Should: report the rejected resend",
			newJournal: func(*testing.T) sagas.Journal {
				return &rejectsSamePointer{MemoryJournal: sagas.NewMemoryJournal(), seen: map[*sagas.Event]bool{}}
			},
			wantFailing: []string{ruleExactResend},
			wantCauses:  []string{"Append of the same event again returned"},
		},
		{
			name: "[Fail] When: the journal accepts a second writer at seq 2 ; Should: report two winners at seq 2",
			newJournal: func(*testing.T) sagas.Journal {
				return &acceptsSecondWriter{MemoryJournal: sagas.NewMemoryJournal(), fromSeq: seqSecond}
			},
			wantFailing: []string{ruleConcurrent},
			wantCauses:  []string{"appends of seq 2 by different writers returned nil, want 1"},
		},
		{
			name: "[Fail] When: the journal swaps the sentinel above seq 1 ; Should: report the loser at seq 2",
			newJournal: func(*testing.T) sagas.Journal {
				return &swapsSentinelForSecondWriter{MemoryJournal: sagas.NewMemoryJournal()}
			},
			wantFailing: []string{ruleConcurrent},
			wantCauses: []string{
				"a losing append of seq 2 returned",
				"want an error wrapping " + sagas.ErrConcurrentWriter.Error(),
			},
		},
	}
}

func holdingEvent(t *testing.T) *sagas.MemoryJournal {
	t.Helper()

	j := sagas.NewMemoryJournal()
	require.NoError(t, j.Append(t.Context(), journalEvent(1)))

	return j
}

func journalRules() []string {
	return []string{
		ruleNextEvent, ruleGap, ruleSeqBelowOne, ruleEqualResend, ruleDifferentEvent, ruleRejectionFirst,
		ruleRejectionLater, ruleStoredEvents, ruleNoEvents, ruleStoredCopy, ruleConcurrent, ruleDoneContext,
		ruleInvalidEvent, ruleUnknownVersion, ruleTimeZone, ruleExactResend,
	}
}

func Test_JournalCases(t *testing.T) {
	t.Parallel()

	for _, tt := range journalRows() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cases, err := JournalCases(t.Context(), tt.newJournal(t))
			if tt.wantProbeErr {
				require.ErrorIs(t, err, ErrJournalContract)
				assert.Nil(t, cases)
				assert.Empty(t, tt.wantFailing)
				assertCauses(t, tt.wantCauses, []string{err.Error()})
				assertWrapped(t, tt.wantWrapped, err)

				return
			}

			require.NoError(t, err)

			var (
				names, failing, reports []string
				runErrs                 []error
			)

			for c := range cases {
				names = append(names, c.Name)

				if runErr := c.Run(t.Context(), tt.newJournal(t)); runErr != nil {
					assert.ErrorIs(t, runErr, ErrJournalContract)
					assert.Contains(t, runErr.Error(), fmt.Sprintf("rule %q", c.Name))

					failing = append(failing, c.Name)
					reports = append(reports, runErr.Error())
					runErrs = append(runErrs, runErr)
				}
			}

			assert.Equal(t, journalRules(), names)
			assert.ElementsMatch(t, tt.wantFailing, failing, "reports: %v", reports)
			assertCauses(t, tt.wantCauses, reports)
			assertWrapped(t, tt.wantWrapped, errors.Join(runErrs...))
		})
	}
}

func assertCauses(t *testing.T, causes, reports []string) {
	t.Helper()

	for _, cause := range causes {
		assert.True(
			t,
			slices.ContainsFunc(reports, func(got string) bool { return strings.Contains(got, cause) }),
			"missing cause %q in %v",
			cause,
			reports,
		)
	}
}

func assertWrapped(t *testing.T, wrapped []error, err error) {
	t.Helper()

	for _, want := range wrapped {
		assert.ErrorIs(t, err, want)
	}
}

func Test_TestJournal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		newJournal func() sagas.Journal
	}{
		{
			name: "[Success] When: the journal is the memory journal ; " +
				"Should: call the factory with the parent t and then once per case with the subtest t",
			newJournal: func() sagas.Journal { return sagas.NewMemoryJournal() },
		},
		{
			name: "[Success] When: the journal reads an empty slice back as nil ; " +
				"Should: call the factory with the parent t and then once per case with the subtest t",
			newJournal: func() sagas.Journal { return readsEmptyAsNil{sagas.NewMemoryJournal()} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			log := &factoryLog{}

			TestJournal(t, func(t *testing.T) sagas.Journal {
				t.Helper()
				log.called(t.Name())

				return tt.newJournal()
			})

			assert.Equal(t, subtestNames(t.Name(), journalRules()), log.names)
		})
	}
}

func zeroFields(v reflect.Value, path string) []string {
	switch {
	case v.Kind() == reflect.Slice && v.Len() == 0:
		return []string{path}
	case v.Kind() == reflect.Slice:
		zero := zeroFields(v.Index(0), path+"[]")
		for i := 1; i < v.Len(); i++ {
			inElement := zeroFields(v.Index(i), path+"[]")
			zero = slices.DeleteFunc(zero, func(p string) bool { return !slices.Contains(inElement, p) })
		}

		return zero
	case v.Kind() == reflect.Struct && v.Type() != reflect.TypeFor[time.Time]():
		var zero []string
		for field, value := range v.Fields() {
			zero = append(zero, zeroFields(value, path+"."+field.Name)...)
		}

		return zero
	case v.IsZero():
		return []string{path}
	default:
		return nil
	}
}

func Test_JournalCases_referenceEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event *sagas.Event
		want  []string
	}{
		{
			name:  "[Success] When: the reference event of the contract ; Should: find no zero field",
			event: journalEvent(1),
		},
		{
			name:  "[Success] When: the other reference event of the contract ; Should: find no zero field",
			event: otherEvent(seqSecond),
		},
		{
			name:  "[Fail] When: the reference event with empty slices ; Should: name only the empty slices",
			event: emptySlicesEvent(seqThird),
			want:  []string{"Event.Err.Previous", "Event.Values", "Event.Steps"},
		},
		{
			name:  "[Fail] When: an event without Status ; Should: name Status",
			event: withoutStatus(journalEvent(1)),
			want:  []string{"Event.Status"},
		},
		{
			name:  "[Fail] When: no step is compensable ; Should: name the Compensable field of the steps",
			event: withoutCompensable(journalEvent(1)),
			want:  []string{"Event.Steps[].Compensable"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, zeroFields(reflect.ValueOf(*tt.event), "Event"))
		})
	}
}

func withoutStatus(e *sagas.Event) *sagas.Event {
	e.Status = ""

	return e
}

func withoutCompensable(e *sagas.Event) *sagas.Event {
	for i := range e.Steps {
		e.Steps[i].Compensable = false
	}

	return e
}
