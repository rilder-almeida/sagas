package sagastest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/rilder-almeida/sagas"
)

// Setup builds a fresh Case over fresh fakes. It passes mw to sagas.WithMiddleware.
type Setup[S any] func(mw ...sagas.Middleware) Case[S]

// Case is one saga under test: the saga, its state, a check of the fakes, the flow given to sagas.New, and Late,
// which sends the action of step again to the fakes with key.
type Case[S any] struct {
	Saga  *sagas.Saga[S]
	State *S
	Check func(*S) error
	Flow  sagas.Flow
	Late  func(ctx context.Context, step, key string) error
}

type plan struct {
	steps       []sagas.StepReport
	pivot       string
	forwardOnly []string
}

func planOf[S any](flow sagas.Flow) (plan, error) {
	steps, err := sagas.Inspect[S](flow)
	if err != nil {
		return plan{}, err
	}

	p := plan{steps: steps}

	if i := slices.IndexFunc(steps, func(s sagas.StepReport) bool { return s.Pivot }); i >= 0 {
		p.pivot = steps[i].Name
	}

	if p.pivot != "" {
		p.forwardOnly = descendants(steps, p.pivot)
	}

	return p, nil
}

func descendants(steps []sagas.StepReport, root string) []string {
	reached := map[string]bool{root: true}

	for grew := true; grew; {
		grew = false

		for i := range steps {
			s := &steps[i]
			if !reached[s.Name] && slices.ContainsFunc(s.After, func(a string) bool { return reached[a] }) {
				reached[s.Name], grew = true, true
			}
		}
	}

	var out []string

	for i := range steps {
		if name := steps[i].Name; name != root && reached[name] {
			out = append(out, name)
		}
	}

	return out
}

func runCase[S any](ctx context.Context, setup Setup[S], mw ...sagas.Middleware) (Case[S], error) {
	c := setup(mw...)

	return c, c.Saga.Run(ctx, c.State)
}

func probe[S any](tb testing.TB, setup Setup[S]) (plan, bool) {
	tb.Helper()

	rec := NewRecorder()

	c, err := runCase(tb.Context(), setup, rec.Middleware())
	if err != nil {
		tb.Errorf("sagastest: the clean run failed: %v", err)

		return plan{}, false
	}

	p, err := planOf[S](c.Flow)
	if err != nil {
		tb.Errorf("sagastest: Case.Flow is not a valid flow: %v", err)

		return plan{}, false
	}

	named := func(step string) bool {
		return slices.ContainsFunc(p.steps, func(s sagas.StepReport) bool { return s.Name == step })
	}

	ok := true

	for _, call := range rec.Calls() {
		if !named(call.Step) {
			tb.Errorf("sagastest: step %q ran in the clean run, but Case.Flow does not name it", call.Step)

			ok = false
		}
	}

	return p, ok
}

const (
	caseNeverApplied = "never applied"
	caseTwice        = "compensation runs twice"
	caseLate         = "step lands after its compensation"
)

var errTrigger = errors.New("sagastest: injected failure of the step action")

// Compensations proves, for every step with Compensate, that its compensation is a no-op when the step never
// applied, is idempotent over a landed step, and tolerates the step landing after it. Each case is a fresh Case.
func Compensations[S any](tb testing.TB, setup Setup[S]) {
	tb.Helper()

	p, ok := probe(tb, setup)
	if !ok {
		return
	}

	var steps []string

	for i := range p.steps {
		if p.steps[i].Compensate {
			steps = append(steps, p.steps[i].Name)
		}
	}

	if len(steps) == 0 {
		tb.Error("sagastest: Compensations found no step with Compensate in Case.Flow")

		return
	}

	for _, step := range steps {
		for _, kind := range []string{caseNeverApplied, caseTwice, caseLate} {
			if !compensationCase(tb, setup, step, kind) {
				break
			}
		}
	}
}

func compensationCase[S any](tb testing.TB, setup Setup[S], step, kind string) bool {
	tb.Helper()

	rec := NewRecorder()
	mw := []sagas.Middleware{rec.Middleware(), Fail(step, sagas.Permanent(errTrigger), OnPhase(sagas.PhaseExecute))}

	if kind == caseTwice {
		mw = []sagas.Middleware{rec.Middleware(), Panic(step, OnPhase(sagas.PhaseExecute)), compensateTwice(step)}
	}

	ctx := tb.Context()
	c, err := runCase(ctx, setup, mw...)

	i := slices.IndexFunc(rec.Calls(), func(call Call) bool { return call.Step == step })
	if i < 0 {
		tb.Errorf("sagastest: step %q, case %q: the step did not start, Run returned %v, "+
			"so its compensation is untested", step, kind, err)

		return false
	}

	if !errors.Is(err, sagas.ErrCompensated) {
		tb.Errorf("sagastest: step %q, case %q: Run returned %v, want sagas.ErrCompensated", step, kind, err)

		return true
	}

	if kind == caseLate && !late(ctx, tb, &c, step, rec.Calls()[i].Key) {
		return true
	}

	if checkErr := c.Check(c.State); checkErr != nil {
		tb.Errorf("sagastest: step %q, case %q: Check: %v", step, kind, checkErr)
	}

	return true
}

func late[S any](ctx context.Context, tb testing.TB, c *Case[S], step, key string) bool {
	tb.Helper()

	if c.Late == nil {
		tb.Errorf("sagastest: step %q, case %q: Case.Late is nil", step, caseLate)

		return false
	}

	if err := c.Late(ctx, step, key); err != nil {
		tb.Logf("sagastest: step %q, case %q: Late: %v", step, caseLate, err)
	}

	return true
}

func compensateTwice(step string) sagas.MiddlewareFunc {
	return func(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
		if info.Step != step || info.Phase != sagas.PhaseCompensate {
			return call(ctx)
		}

		if err := call(ctx); err != nil {
			return err
		}

		return call(ctx)
	}
}
