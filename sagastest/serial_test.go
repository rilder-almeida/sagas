package sagastest

import (
	"context"
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

func Test_WithoutConcurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		steps []string
		want  []string
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

					saga, err := sagas.New[probeState]("serial", sagas.Parallel(flows...))
					require.NoError(t, err)
					require.NoError(t, saga.Run(WithoutConcurrency(t.Context()), &probeState{ID: runID}))
					assert.Equal(t, tt.want, tr.snapshot())
				}
			})
		})
	}
}
