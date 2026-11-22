package sagastest

import (
	"context"
	"slices"

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
