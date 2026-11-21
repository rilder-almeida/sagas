package sagas

import (
	"fmt"
	"reflect"
	"slices"
)

// Flow is a step or a group of steps in the order they run. A pointer to a type that embeds Step is a Flow, and
// Sequence and Parallel build the groups. One pointer is one step, wherever it appears.
type Flow interface {
	flowStep() *Step
}

func (s *Step) flowStep() *Step { return s }

// Group is a Flow of several items that Sequence or Parallel builds.
type Group struct {
	kind  string
	items []Flow
}

func (*Group) flowStep() *Step { return nil }

// Sequence runs items one after another: each item starts after every step that ends the item before it.
func Sequence(items ...Flow) *Group {
	return &Group{kind: "Sequence", items: slices.Clone(items)}
}

// Parallel runs items at the same time and ends after the last one ends.
func Parallel(items ...Flow) *Group {
	return &Group{kind: "Parallel", items: slices.Clone(items)}
}

type ends struct {
	heads []int
	tails []int
}

type resolution struct {
	flows   []Flow
	parents [][]int
	index   map[*Step]int
	errs    []error
}

func resolve(flow Flow) resolution {
	r := resolution{index: map[*Step]int{}}

	if !isNilValue(flow) {
		r.visit(flow, "flow")
	}

	return r
}

func (r *resolution) visit(f Flow, at string) ends {
	if isNilValue(f) {
		r.errs = append(r.errs, fmt.Errorf("%w: %s is nil", ErrInvalidStep, at))

		return ends{}
	}

	g, ok := f.(*Group)
	if !ok {
		return r.step(f, at)
	}

	var out ends

	for i, item := range g.items {
		e := r.visit(item, fmt.Sprintf("%s item at index %d", g.kind, i))

		switch {
		case g.kind == "Parallel":
			out.heads = append(out.heads, e.heads...)
			out.tails = append(out.tails, e.tails...)
		case len(e.heads) == 0:
		default:
			for _, h := range e.heads {
				r.link(h, out.tails)
			}

			if out.heads == nil {
				out.heads = e.heads
			}

			out.tails = e.tails
		}
	}

	return out
}

func (r *resolution) step(f Flow, at string) ends {
	if t := reflect.TypeOf(f); !embedsStep(t) {
		r.errs = append(r.errs,
			fmt.Errorf("%w: %s has type %s, want a pointer to a struct that embeds Step", ErrInvalidStep, at, t))

		return ends{}
	}

	s := f.flowStep()

	i, seen := r.index[s]
	if !seen {
		i = len(r.flows)
		r.index[s] = i
		r.flows = append(r.flows, f)
		r.parents = append(r.parents, []int{})
	}

	return ends{heads: []int{i}, tails: []int{i}}
}

func embedsStep(t reflect.Type) bool {
	if t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return false
	}

	f, ok := t.Elem().FieldByName("Step")
	if !ok || !f.Anonymous || f.Type != reflect.TypeFor[Step]() {
		return false
	}

	outer := t.Elem()
	for _, i := range f.Index[:len(f.Index)-1] {
		outer = outer.Field(i).Type
		if outer.Kind() != reflect.Struct {
			return false
		}
	}

	return true
}

func (r *resolution) link(child int, parents []int) {
	for _, p := range parents {
		if !slices.Contains(r.parents[child], p) {
			r.parents[child] = append(r.parents[child], p)
		}
	}
}
