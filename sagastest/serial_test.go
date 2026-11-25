package sagastest

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	startA = "start a"
	endA   = "end a"
	startB = "start b"
	endB   = "end b"
	startC = "start c"
	endC   = "end c"
)

var errLastFails = errors.New("last step fails")

type trace struct {
	mu    sync.Mutex
	marks []string
}

func (t *trace) add(mark string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.marks = append(t.marks, mark)
}

func (t *trace) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return slices.Clone(t.marks)
}

type traceStep struct {
	sagas.Step[probeState, sagas.None]

	trace *trace
}

func (s *traceStep) Execute(context.Context, probeState) error {
	s.trace.add("start " + s.Name)
	time.Sleep(time.Millisecond)
	s.trace.add("end " + s.Name)

	return nil
}

func (s *traceStep) Compensate(context.Context, probeState) error {
	s.trace.add("undo start " + s.Name)
	time.Sleep(time.Millisecond)
	s.trace.add("undo end " + s.Name)

	return nil
}

func Test_WithoutConcurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		steps   []string
		fail    bool
		wantErr error
		want    []string
	}{
		{
			name: "[Success] When: two parallel steps run under the mark ; " +
				"Should: run them one at a time in graph order",
			steps: []string{"a", "b"},
			want:  []string{startA, endA, startB, endB},
		},
		{
			name: "[Success] When: three parallel steps run under the mark ; " +
				"Should: run them one at a time in graph order",
			steps: []string{"a", "b", "c"},
			want:  []string{startA, endA, startB, endB, startC, endC},
		},
		{
			name: "[Fail] When: a step after three parallel compensable steps fails under the mark ; " +
				"Should: run the compensations one at a time in reverse graph order",
			steps:   []string{"a", "b", "c"},
			fail:    true,
			wantErr: sagas.ErrCompensated,
			want: []string{
				startA, endA, startB, endB, startC, endC,
				"undo start c", "undo end c", "undo start b", "undo end b", "undo start a", "undo end a",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				for range 50 {
					tr := &trace{}

					flows := make([]sagas.Flow, 0, len(tt.steps))
					for _, name := range tt.steps {
						s := &traceStep{trace: tr}
						s.Name = name
						flows = append(flows, s)
					}

					flow := sagas.Flow(sagas.Parallel(flows...))
					if tt.fail {
						flow = sagas.Sequence(flow, newProbe("x", sagas.NoRetry(), errLastFails))
					}

					saga, err := sagas.New[probeState]("serial", flow)
					require.NoError(t, err)
					require.ErrorIs(t, saga.Run(WithoutConcurrency(t.Context()), &probeState{ID: runID}), tt.wantErr)
					assert.Equal(t, tt.want, tr.snapshot())
				}
			})
		})
	}
}
