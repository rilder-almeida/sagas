package sagastest

import (
	"context"
	"testing"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type groupBuilder func(items ...sagas.Flow) *sagas.Group

func runRecorded(t *testing.T, group groupBuilder, failing string, names ...string) ([]Call, error) {
	t.Helper()

	rec := NewRecorder()
	steps := make([]sagas.Flow, len(names))

	for i, name := range names {
		var err error
		if name == failing {
			err = errBoom
		}

		steps[i] = newProbe(name, sagas.NoRetry(), err)
	}

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
				calls, err := runRecorded(t, sagas.Sequence, "", tt.names...)
				require.NoError(t, err)
				assert.Len(t, calls, tt.wantSeen)
			}

			assert.Empty(t, fresh.Calls())
		})
	}
}

func Test_Recorder_Middleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		flow      groupBuilder
		failing   string
		names     []string
		wantCalls []Call
		wantErr   bool
		unordered bool
	}{
		{
			name:    "[Success] When: three steps run in sequence ; Should: record them in order with the shared key",
			flow:    sagas.Sequence,
			failing: "",
			names:   []string{"a", "b", "c"},
			wantCalls: []Call{
				{Step: "a", Phase: sagas.PhaseExecute, Attempt: 1, Key: keyA},
				{Step: "b", Phase: sagas.PhaseExecute, Attempt: 1, Key: keyB},
				{Step: "c", Phase: sagas.PhaseExecute, Attempt: 1, Key: keyC},
			},
		},
		{
			name:    "[Fail] When: the last step fails ; Should: record the error it returned",
			flow:    sagas.Sequence,
			failing: "c",
			names:   []string{"a", "b", "c"},
			wantCalls: []Call{
				{Step: "a", Phase: sagas.PhaseExecute, Attempt: 1, Key: keyA},
				{Step: "b", Phase: sagas.PhaseExecute, Attempt: 1, Key: keyB},
				{Step: "c", Phase: sagas.PhaseExecute, Attempt: 1, Key: keyC, Err: errBoom},
			},
			wantErr: true,
		},
		{
			name:    "[Success] When: two steps run in parallel ; Should: record both without a data race",
			flow:    sagas.Parallel,
			failing: "",
			names:   []string{"a", "b"},
			wantCalls: []Call{
				{Step: "a", Phase: sagas.PhaseExecute, Attempt: 1, Key: keyA},
				{Step: "b", Phase: sagas.PhaseExecute, Attempt: 1, Key: keyB},
			},
			unordered: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := runRecorded(t, tt.flow, tt.failing, tt.names...)

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
			probe := newProbe("a", sagas.NoRetry(), nil)
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
