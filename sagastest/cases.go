package sagastest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/rilder-almeida/sagas"
)

// Services is the test type that holds the fakes of one case. Flow returns the flow over those fakes. Check checks the
// fakes and the final state after the run. If the type also has an Options() []sagas.Option method, the kit builds the
// saga with those options and adds its own middleware after them. For a saga with an observer, the factory makes the
// queue with sagas.NewMemoryEventQueue, passes it in Options with sagas.WithEventQueue, and closes it with Close in
// t.Cleanup.
type Services[S any] interface {
	Flow() *sagas.Group
	Check(ctx context.Context, final *S) error
}

// CompensationServices is a Services with a late action. ExecuteLate runs the action of step again on the fakes, with
// key, after the compensation of step ran.
type CompensationServices[S any] interface {
	Services[S]
	ExecuteLate(ctx context.Context, state *S, step, key string) error
}

// Case is one named case that a case list of this package builds. Run panics on a Case built another way.
type Case[W any] struct {
	Name string
	run  func(ctx context.Context, services W) error
}

// Run runs the case on services. Its error wraps the sentinel error of the failure kind.
func (c Case[W]) Run(ctx context.Context, services W) error {
	if c.run == nil {
		panic("sagastest: Case not built by a case list")
	}

	return c.run(ctx, services)
}

var (
	// ErrCleanRun reports a clean run that failed, recorded no call, or ran a step that the flow does not name.
	ErrCleanRun = errors.New("sagastest: clean run failed")
	// ErrNoStepToTest reports a flow with no step that the kit tests.
	ErrNoStepToTest = errors.New("sagastest: no step to test")
	// ErrNotStarted reports a step that the case exercises and that did not start.
	ErrNotStarted = errors.New("sagastest: step did not start")
	// ErrOutcome reports a run with the wrong error, the wrong pending steps, a compensation after an unknown pivot, or
	// the wrong final state.
	ErrOutcome = errors.New("sagastest: wrong outcome")
	// ErrCheck reports a failed Check.
	ErrCheck = errors.New("sagastest: check failed")
	// ErrJournalContract reports a journal that breaks a rule of the journal contract.
	ErrJournalContract = errors.New("sagastest: journal contract broken")
	// ErrSecretStoreContract reports a secret store that breaks a rule of the secret store contract.
	ErrSecretStoreContract = errors.New("sagastest: secret store contract broken")
)

type optioner interface {
	Options() []sagas.Option
}

func runCase[S any, W Services[S]](
	ctx context.Context, name string, state S, services W, rec *Recorder, faults ...sagas.Middleware,
) (*S, error) {
	var opts []sagas.Option
	if o, ok := any(services).(optioner); ok {
		opts = slices.Clip(o.Options())
	}

	kit := sagas.WithMiddleware(append([]sagas.Middleware{rec}, faults...)...)

	saga, err := sagas.New[S](name, services.Flow(), append(opts, kit)...)
	if err != nil {
		return nil, err
	}

	final := state

	return &final, saga.Run(WithoutConcurrency(ctx), &final)
}

func kitFailure(sentinel error, subject, what string, cause error) error {
	err := fmt.Errorf("%w: %s: %s", sentinel, subject, what)
	if cause == nil {
		return err
	}

	return fmt.Errorf("%w: %w", err, cause)
}

func ruleFailure(sentinel error, rule, what string, cause error) error {
	return kitFailure(sentinel, "rule "+strconv.Quote(rule), what, cause)
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
