package sagastest

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_planOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		flow        sagas.Flow
		wantSteps   []string
		wantPivot   string
		wantForward []string
		wantErr     error
	}{
		{
			name: "[Success] When: two forward-only steps run in parallel after the pivot ; " +
				"Should: list both in the forward-only zone",
			flow: sagas.Sequence(
				newCharge(newLedger(sound)), newPivotStep(), sagas.Parallel(newForwardShip(nil), newNotify()),
			),
			wantSteps:   []string{stepCharge, stepPivot, stepShip, stepNotify},
			wantPivot:   stepPivot,
			wantForward: []string{stepShip, stepNotify},
		},
		{
			name: "[Success] When: a step runs two hops after the pivot ; " +
				"Should: list it in the forward-only zone too",
			flow: sagas.Sequence(
				newCharge(newLedger(sound)), newPivotStep(), newForwardShip(nil), newNotify(),
			),
			wantSteps:   []string{stepCharge, stepPivot, stepShip, stepNotify},
			wantPivot:   stepPivot,
			wantForward: []string{stepShip, stepNotify},
		},
		{
			name:      "[Success] When: the flow has no pivot ; Should: give no pivot name and an empty zone",
			flow:      sagas.Sequence(newCharge(newLedger(sound)), newShip(nil)),
			wantSteps: []string{stepCharge, stepShip},
		},
		{
			name:      "[Success] When: a step has Enabled ; Should: still list it among the steps",
			flow:      sagas.Sequence(newDisabledCharge(newLedger(sound)), newShip(nil)),
			wantSteps: []string{stepCharge, stepShip},
		},
		{
			name:    "[Fail] When: a step after the pivot has Compensate ; Should: return the error of Inspect",
			flow:    sagas.Sequence(newPivotStep(), newCharge(newLedger(sound))),
			wantErr: sagas.ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := planOf[probeState](tt.flow)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			names := make([]string, 0, len(p.steps))
			for _, s := range p.steps {
				names = append(names, s.Name)
			}

			assert.Equal(t, tt.wantSteps, names)
			assert.Equal(t, tt.wantPivot, p.pivot)
			assert.Equal(t, tt.wantForward, p.forwardOnly)
		})
	}
}

func Test_Case_Run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		defect   defect
		kind     string
		wantName string
		wantErr  error
	}{
		{
			name:     "[Success] When: the refund of a charge that never landed is a no-op ; Should: return nil",
			kind:     caseNeverApplied,
			wantName: "charge never applied",
		},
		{
			name: "[Fail] When: the refund fails on a charge that never landed ; " +
				"Should: return an error that wraps ErrOutcome",
			defect:   refundFailsUnapplied,
			kind:     caseNeverApplied,
			wantName: "charge never applied",
			wantErr:  ErrOutcome,
		},
		{
			name:     "[Fail] When: the refund posts twice ; Should: return an error that wraps ErrCheck",
			defect:   refundTwice,
			kind:     caseTwice,
			wantName: "charge compensation runs twice",
			wantErr:  ErrCheck,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := compensationCase[probeState, *shopServices]("shop", probeState{ID: runID}, stepCharge, tt.kind)
			err := c.Run(t.Context(), newShopServices(tt.defect, 1, chargeThenShip, lateCharge))

			assert.Equal(t, tt.wantName, c.Name)

			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

type traceServices struct {
	trace *trace
	steps []string
}

func (s *traceServices) Flow() *sagas.Group {
	flows := make([]sagas.Flow, 0, len(s.steps))
	for _, name := range s.steps {
		step := &traceStep{trace: s.trace}
		step.Name = name
		flows = append(flows, step)
	}

	return sagas.Parallel(flows...)
}

func (*traceServices) Check(context.Context, *probeState) error { return nil }

type optionsServices struct {
	traceServices

	opts []sagas.Option
}

func (s *optionsServices) Options() []sagas.Option { return s.opts }

func stepsOf(rec *Recorder) []string {
	attempts := rec.Attempts()

	steps := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		steps = append(steps, attempt.Step)
	}

	return steps
}

func Test_runCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		steps       []string
		services    func(base traceServices, own *Recorder, journal sagas.Journal) Services[probeState]
		want        []string
		wantOwn     []string
		wantJournal bool
	}{
		{
			name: "[Success] When: the services have no Options method and two parallel steps ; " +
				"Should: run them one at a time in graph order",
			steps: []string{"a", "b"},
			services: func(base traceServices, _ *Recorder, _ sagas.Journal) Services[probeState] {
				return &base
			},
			want:    []string{startA, endA, startB, endB},
			wantOwn: []string{},
		},
		{
			name: "[Success] When: the services have no Options method and three parallel steps ; " +
				"Should: run them one at a time in graph order",
			steps: []string{"a", "b", "c"},
			services: func(base traceServices, _ *Recorder, _ sagas.Journal) Services[probeState] {
				return &base
			},
			want:    []string{startA, endA, startB, endB, startC, endC},
			wantOwn: []string{},
		},
		{
			name:  "[Success] When: Options returns WithJournal ; Should: record the run in that journal",
			steps: []string{"a", "b"},
			services: func(base traceServices, _ *Recorder, journal sagas.Journal) Services[probeState] {
				return &optionsServices{traceServices: base, opts: []sagas.Option{sagas.WithJournal(journal)}}
			},
			want:        []string{startA, endA, startB, endB},
			wantOwn:     []string{},
			wantJournal: true,
		},
		{
			name: "[Success] When: Options returns its own WithMiddleware ; " +
				"Should: let both that middleware and the kit middleware see each call",
			steps: []string{"a", "b"},
			services: func(base traceServices, own *Recorder, _ sagas.Journal) Services[probeState] {
				return &optionsServices{traceServices: base, opts: []sagas.Option{sagas.WithMiddleware(own)}}
			},
			want:    []string{startA, endA, startB, endB},
			wantOwn: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				tr, own, kit, journal := &trace{}, NewRecorder(), NewRecorder(), sagas.NewMemoryJournal()
				services := tt.services(traceServices{trace: tr, steps: tt.steps}, own, journal)

				final, err := runCase(t.Context(), "serial", probeState{ID: runID}, services, kit)
				require.NoError(t, err)

				events, journalErr := journal.Events(t.Context(), runID)

				assert.Equal(t, probeState{ID: runID}, *final)
				assert.Equal(t, tt.want, tr.snapshot())
				assert.Equal(t, tt.steps, stepsOf(kit))
				assert.Equal(t, tt.wantOwn, stepsOf(own))
				assert.Equal(t, tt.wantJournal, journalErr == nil && len(events) > 0)
			})
		})
	}
}
