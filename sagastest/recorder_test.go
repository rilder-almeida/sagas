package sagastest

import (
	"context"
	"testing"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type groupBuilder func(items ...sagas.Flow) *sagas.Group

func runRecorded(t *testing.T, group groupBuilder, steps ...sagas.Flow) ([]Call, error) {
	t.Helper()

	rec := NewRecorder()

	saga, err := sagas.New[probeState]("recorded", group(steps...), sagas.WithMiddleware(rec.Middleware()))
	require.NoError(t, err)

	runErr := saga.Run(context.Background(), &probeState{})

	return rec.Calls(), runErr
}

const (
	keyA = "run-1/a"
	keyB = "run-1/b"
	keyC = "run-1/c"
)

func Test_NewRecorder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		names    []string
		wantSeen int
	}{
		{
			name:     "[Success] When: nothing ran ; Should: return no calls",
			names:    nil,
			wantSeen: 0,
		},
		{
			name:     "[Success] When: another recorder saw two steps ; Should: return no calls",
			names:    []string{"a", "b"},
			wantSeen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fresh := NewRecorder()

			if len(tt.names) > 0 {
				calls, err := runRecorded(t, sagas.Sequence, plain("a"), plain("b"))
				require.NoError(t, err)
				assert.Len(t, calls, tt.wantSeen)
			}

			assert.Empty(t, fresh.Calls())
		})
	}
}

func Test_Recorder_Middleware(t *testing.T) {
	t.Parallel()

	exec := func(step, key string, attempt int, err error) Call {
		return Call{Step: step, Phase: sagas.PhaseExecute, Attempt: attempt, Key: key, Err: err}
	}

	tests := []struct {
		name      string
		flow      groupBuilder
		build     func() []sagas.Flow
		wantCalls []Call
		wantErr   bool
		unordered bool
	}{
		{
			name: "[Success] When: three steps run in sequence ; Should: record them in order with the shared key",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{
					plain("a"), plain("b"), plain("c"),
				}
			},
			wantCalls: []Call{exec("a", keyA, 1, nil), exec("b", keyB, 1, nil), exec("c", keyC, 1, nil)},
		},
		{
			name: "[Fail] When: the last step fails ; Should: record the error it returned",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{
					plain("a"), plain("b"), newProbe("c", sagas.NoRetry(), errBoom),
				}
			},
			wantCalls: []Call{exec("a", keyA, 1, nil), exec("b", keyB, 1, nil), exec("c", keyC, 1, errBoom)},
			wantErr:   true,
		},
		{
			name: "[Success] When: two steps run in parallel ; Should: record both without a data race",
			flow: sagas.Parallel,
			build: func() []sagas.Flow {
				return []sagas.Flow{plain("a"), plain("b")}
			},
			wantCalls: []Call{exec("a", keyA, 1, nil), exec("b", keyB, 1, nil)},
			unordered: true,
		},
		{
			name: "[Fail] When: a step retries ; Should: record each attempt number with its error",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{newProbe("a", fastRetry(), errBoom)}
			},
			wantCalls: []Call{exec("a", keyA, 1, errBoom), exec("a", keyA, 2, errBoom)},
			wantErr:   true,
		},
		{
			name: "[Fail] When: a later step fails ; Should: record the compensation of the earlier step",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{newUndo("a", nil), newProbe("b", sagas.NoRetry(), errBoom)}
			},
			wantCalls: []Call{
				exec("a", keyA, 1, nil),
				exec("b", keyB, 1, errBoom),
				{Step: "a", Phase: sagas.PhaseCompensate, Attempt: 1, Key: keyA},
			},
			wantErr: true,
		},
		{
			name: "[Success] When: a step has a pivot ; Should: record the pivot phase",
			flow: sagas.Sequence,
			build: func() []sagas.Flow {
				return []sagas.Flow{newPivot("a")}
			},
			wantCalls: []Call{{Step: "a", Phase: sagas.PhasePivot, Attempt: 1, Key: keyA}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := runRecorded(t, tt.flow, tt.build()...)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			if tt.unordered {
				assert.ElementsMatch(t, tt.wantCalls, calls)
			} else {
				assert.Equal(t, tt.wantCalls, calls)
			}
		})
	}
}

func Test_Recorder_Calls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		corrupt func([]Call) []Call
	}{
		{
			name:    "[Success] When: the caller rewrites an element ; Should: leave a second Calls unchanged",
			corrupt: func(c []Call) []Call { c[0].Step = "changed"; return c },
		},
		{
			name:    "[Success] When: the caller truncates and appends ; Should: leave a second Calls unchanged",
			corrupt: func(c []Call) []Call { return append(c[:0], Call{Step: "x"}) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := NewRecorder()
			probe := plain("a")
			saga, err := sagas.New[probeState]("copy", probe, sagas.WithMiddleware(rec.Middleware()))
			require.NoError(t, err)
			require.NoError(t, saga.Run(context.Background(), &probeState{}))

			first := rec.Calls()
			want := []Call{{Step: "a", Phase: sagas.PhaseExecute, Attempt: 1, Key: keyA}}
			require.Equal(t, want, first)

			tt.corrupt(first)

			assert.Equal(t, want, rec.Calls())
		})
	}
}
