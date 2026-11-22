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

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
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

type acceptsGap struct{ *sagas.MemoryJournal }

func (j acceptsGap) Append(ctx context.Context, e *sagas.Event) error {
	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	if e.Seq > len(j.Events(e.Execution))+1 {
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

	if e.Seq == 1 && len(j.Events(e.Execution)) > 0 {
		return fmt.Errorf("%w: resend", sagas.ErrExecutionExists)
	}

	if e.Seq > 1 && e.Seq <= len(j.Events(e.Execution)) {
		return fmt.Errorf("%w: resend", sagas.ErrConcurrentWriter)
	}

	return j.MemoryJournal.Append(ctx, e)
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

	if e.Seq >= 1 && e.Seq <= len(j.Events(e.Execution)) {
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

	mu sync.Mutex
}

func (j *acceptsSecondWriter) Append(ctx context.Context, e *sagas.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if e == nil {
		return j.MemoryJournal.Append(ctx, e)
	}

	stored := j.Events(e.Execution)
	if e.Seq >= 1 && e.Seq <= len(stored) && stored[e.Seq-1].Writer != e.Writer {
		return nil
	}

	return j.MemoryJournal.Append(ctx, e)
}

type acceptsGapOnEmpty struct{ *sagas.MemoryJournal }

func (j acceptsGapOnEmpty) Append(ctx context.Context, e *sagas.Event) error {
	if e != nil && e.Seq > 1 && len(j.Events(e.Execution)) == 0 {
		return nil
	}

	return j.MemoryJournal.Append(ctx, e)
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
	if e != nil && e.Seq >= 1 && e.Seq <= len(j.Events(e.Execution)) &&
		j.Events(e.Execution)[e.Seq-1].Time != e.Time {
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

func Test_Journal(t *testing.T) {
	t.Parallel()

	shared := sagas.NewMemoryJournal()

	tests := []struct {
		name       string
		newJournal func() sagas.Journal
		wantRules  []string
		wantCauses []string
	}{
		{
			name:       "[Success] When: the journal is the memory journal ; Should: report nothing",
			newJournal: func() sagas.Journal { return sagas.NewMemoryJournal() },
		},
		{
			name:       "[Fail] When: newJournal returns one journal ; Should: report a journal that is not fresh",
			newJournal: func() sagas.Journal { return shared },
			wantRules:  []string{ruleFresh},
			wantCauses: []string{"returned a journal that is not empty"},
		},
		{
			name: "[Fail] When: the journal keeps the caller pointer ; Should: report an accepted changed resend",
			newJournal: func() sagas.Journal {
				return &keepsPointer{MemoryJournal: sagas.NewMemoryJournal(), kept: map[string]*sagas.Event{}}
			},
			wantRules: []string{ruleStoredCopy},
			wantCauses: []string{
				"resend of seq 1 after the caller changed its scalar field returned nil",
				"resend of seq 1 after the caller changed its Changed slice returned nil",
			},
		},
		{
			name: "[Fail] When: the journal keeps the caller slices ; Should: report an accepted changed resend",
			newJournal: func() sagas.Journal {
				return &keepsSlices{MemoryJournal: sagas.NewMemoryJournal(), kept: map[string]sagas.Event{}}
			},
			wantRules: []string{ruleStoredCopy},
			wantCauses: []string{
				"resend of seq 1 after the caller changed its Steps slice returned nil",
				"resend of seq 1 after the caller changed its Err.Previous slice returned nil",
			},
		},
		{
			name:       "[Fail] When: the journal accepts a gap ; Should: report that seq 3 after seq 1 was accepted",
			newJournal: func() sagas.Journal { return acceptsGap{sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleGap},
			wantCauses: []string{"Append of seq 3 after seq 1 returned nil"},
		},
		{
			name:       "[Fail] When: the journal accepts seq 2 on an empty journal ; Should: report seq 2",
			newJournal: func() sagas.Journal { return acceptsGapOnEmpty{sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleGap},
			wantCauses: []string{"Append of seq 2 on an empty journal returned nil"},
		},
		{
			name:       "[Fail] When: the journal accepts a Seq below 1 ; Should: report the accepted seq 0 and seq -1",
			newJournal: func() sagas.Journal { return acceptsLowSeq{sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleSeqBelowOne, ruleInvalidEvent},
			wantCauses: []string{
				"Append of seq 0 returned nil", "Append of seq -1 returned nil",
				"Append of an event with seq 0 returned nil",
			},
		},
		{
			name:       "[Fail] When: the journal rejects an equal resend ; Should: report the rejected equal copy",
			newJournal: func() sagas.Journal { return rejectsEqualResend{sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleEqualResend, ruleStoredCopy, ruleTimeZone, ruleExactResend},
			wantCauses: []string{
				"Append of an equal copy of seq 1 returned",
				"resend of an unchanged copy of seq 1 after the caller changed its scalar field returned",
				"Append of an equal instant in another zone returned",
				"Append of the same event again returned",
			},
		},
		{
			name:       "[Fail] When: the journal accepts another event at a taken Seq ; Should: report it",
			newJournal: func() sagas.Journal { return &acceptsOtherEvent{MemoryJournal: sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleDifferentEvent, ruleStoredCopy, ruleConcurrent},
			wantCauses: []string{
				"Append of another event at taken seq 1 returned nil",
				"after the caller changed its scalar field returned nil",
				"returned nil, want exactly 1",
			},
		},
		{
			name: "[Fail] When: the journal returns ErrConcurrentWriter at Seq 1 ; Should: report the wrong sentinel",
			newJournal: func() sagas.Journal {
				return swapsSentinel{sagas.NewMemoryJournal(), sagas.ErrExecutionExists, sagas.ErrConcurrentWriter}
			},
			wantRules: []string{ruleRejectionFirst, ruleConcurrent},
			wantCauses: []string{
				"Append of another event at taken seq 1 returned",
				"want an error wrapping " + sagas.ErrExecutionExists.Error(),
				"a losing append of seq 1 returned",
			},
		},
		{
			name: "[Fail] When: the journal returns ErrExecutionExists later ; Should: report the wrong sentinel",
			newJournal: func() sagas.Journal {
				return swapsSentinel{sagas.NewMemoryJournal(), sagas.ErrConcurrentWriter, sagas.ErrExecutionExists}
			},
			wantRules: []string{ruleRejectionLater},
			wantCauses: []string{
				"Append of another event at taken seq 2 returned",
				"want an error wrapping " + sagas.ErrConcurrentWriter.Error(),
			},
		},
		{
			name:       "[Fail] When: the journal accepts a second writer at a taken Seq ; Should: report two winners",
			newJournal: func() sagas.Journal { return &acceptsSecondWriter{MemoryJournal: sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleConcurrent},
			wantCauses: []string{"returned nil, want exactly 1"},
		},
		{
			name:       "[Fail] When: the journal ignores a done context ; Should: report the nil error",
			newJournal: func() sagas.Journal { return ignoresContext{sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleDoneContext},
			wantCauses: []string{"with a done context returned nil", "after a done context returned"},
		},
		{
			name:       "[Fail] When: the journal stores then fails with ctx ; Should: report the rejected event",
			newJournal: func() sagas.Journal { return storesThenFails{sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleDoneContext},
			wantCauses: []string{"after a done context returned"},
		},
		{
			name:       "[Fail] When: the journal accepts a nil event ; Should: report the nil event",
			newJournal: func() sagas.Journal { return acceptsNil{sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleInvalidEvent},
			wantCauses: []string{"Append of a nil event returned nil"},
		},
		{
			name:       "[Fail] When: the journal compares Time with == ; Should: report the rejected equal instant",
			newJournal: func() sagas.Journal { return comparesTimeWithEquals{sagas.NewMemoryJournal()} },
			wantRules:  []string{ruleTimeZone},
			wantCauses: []string{"in another zone returned"},
		},
		{
			name: "[Fail] When: the journal rejects the exact resend ; Should: report the rejected resend",
			newJournal: func() sagas.Journal {
				return &rejectsSamePointer{MemoryJournal: sagas.NewMemoryJournal(), seen: map[*sagas.Event]bool{}}
			},
			wantRules:  []string{ruleExactResend},
			wantCauses: []string{"Append of the same event again returned"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeTB{TB: t}
			Journal(fake, tt.newJournal)

			for _, got := range fake.errors {
				assert.True(
					t,
					slices.ContainsFunc(
						tt.wantRules,
						func(rule string) bool { return strings.Contains(got, fmt.Sprintf("rule %q", rule)) },
					),
					"unexpected report: %s",
					got,
				)
			}

			for _, rule := range tt.wantRules {
				assert.True(
					t,
					slices.ContainsFunc(
						fake.errors,
						func(got string) bool { return strings.Contains(got, fmt.Sprintf("rule %q", rule)) },
					),
					"missing report of %s in %v",
					rule,
					fake.errors,
				)
			}

			for _, cause := range tt.wantCauses {
				assert.True(
					t,
					slices.ContainsFunc(fake.errors, func(got string) bool { return strings.Contains(got, cause) }),
					"missing cause %q in %v",
					cause,
					fake.errors,
				)
			}
		})
	}
}
