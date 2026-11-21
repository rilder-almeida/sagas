package sagas

import (
	"fmt"
	"slices"
)

func indexSteps(named []namedStep) []error {
	first := make(map[string]int, len(named))

	var errs []error

	for i, n := range named {
		at, seen := first[n.name]
		if !seen {
			first[n.name] = i

			continue
		}

		err := fmt.Errorf("%w: step %q: declared more than once", ErrDuplicateStep, n.name)
		if n.derived && named[at].derived && n.typ == named[at].typ {
			err = fmt.Errorf("%w; set Name on each step of type %s", err, n.typ)
		}

		errs = append(errs, err)
	}

	return errs
}

func childrenOf(parents [][]int) [][]int {
	children := make([][]int, len(parents))
	for i := range children {
		children[i] = []int{}
	}

	for i, ps := range parents {
		for _, p := range ps {
			children[p] = append(children[p], i)
		}
	}

	return children
}

type ordering struct {
	order []int
	cycle []int
}

func topoOrder(parents, children [][]int) ordering {
	waiting := make([]int, len(parents))
	order := make([]int, 0, len(parents))

	var ready []int

	for i, ps := range parents {
		waiting[i] = len(ps)
		if waiting[i] == 0 {
			ready = append(ready, i)
		}
	}

	for len(ready) > 0 {
		next := ready[0]
		ready = ready[1:]

		order = append(order, next)

		for _, c := range children[next] {
			waiting[c]--
			if waiting[c] == 0 {
				at, _ := slices.BinarySearch(ready, c)
				ready = slices.Insert(ready, at, c)
			}
		}
	}

	var cycle []int

	for i, w := range waiting {
		if w > 0 {
			cycle = append(cycle, i)
		}
	}

	return ordering{order: order, cycle: cycle}
}

func reach(adj [][]int, from int) []bool {
	seen := make([]bool, len(adj))
	stack := slices.Clone(adj[from])

	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if seen[n] {
			continue
		}

		seen[n] = true

		stack = append(stack, adj[n]...)
	}

	return seen
}

func zones(children [][]int, pivot int) []Zone {
	out := make([]Zone, len(children))
	for i := range out {
		out[i] = ZoneCompensable
	}

	if pivot < 0 {
		return out
	}

	for i, after := range reach(children, pivot) {
		if after {
			out[i] = ZoneForwardOnly
		}
	}

	out[pivot] = ZonePivot

	return out
}

func pivotsOf(reports []StepReport) []int {
	var pivots []int

	for i := range reports {
		if reports[i].Pivot {
			pivots = append(pivots, i)
		}
	}

	return pivots
}

func checkPivot(reports []StepReport, pivots []int, g *graph, graphErrs []error) []error {
	var errs []error

	for _, p := range pivots {
		if reports[p].Compensate {
			errs = append(errs, fmt.Errorf("%w: step %q: pivot has Compensate", ErrInvalidStep, reports[p].Name))
		}

		if reports[p].Enabled {
			errs = append(errs, fmt.Errorf("%w: step %q: pivot has Enabled", ErrInvalidStep, reports[p].Name))
		}
	}

	switch {
	case len(pivots) > 1:
		names := make([]string, 0, len(pivots))
		for _, p := range pivots {
			names = append(names, fmt.Sprintf("%q", reports[p].Name))
		}

		errs = append(errs, fmt.Errorf("%w: more than one Pivot, on steps %v", ErrInvalidStep, names))
	case len(pivots) == 1 && len(graphErrs) == 0:
		errs = append(errs, checkReach(reports, g, pivots[0])...)
	default:
	}

	return errs
}

func checkReach(reports []StepReport, g *graph, pivot int) []error {
	before := reach(g.parents, pivot)
	after := reach(g.children, pivot)

	var errs []error

	for i := range reports {
		switch {
		case i != pivot && !before[i] && !after[i]:
			errs = append(errs, fmt.Errorf("%w: step %q: neither before nor after the pivot %q",
				ErrInvalidStep, reports[i].Name, reports[pivot].Name))
		case after[i] && reports[i].Compensate:
			errs = append(errs, fmt.Errorf("%w: step %q: a step after the pivot has Compensate",
				ErrInvalidStep, reports[i].Name))
		default:
		}
	}

	return errs
}

type graph struct {
	parents  [][]int
	children [][]int
	order    []int
	pivot    int
}

func checkGraph(reports []StepReport, parents [][]int) (graph, []error) {
	g := graph{parents: parents, children: childrenOf(parents), pivot: -1}

	o := topoOrder(g.parents, g.children)
	g.order = o.order

	var errs []error

	if len(o.cycle) > 0 {
		errs = append(errs, cycleError(reports, g.children, o.cycle))
	}

	pivots := pivotsOf(reports)
	if len(pivots) == 1 {
		g.pivot = pivots[0]
	}

	errs = append(errs, checkPivot(reports, pivots, &g, errs)...)

	return g, errs
}

func cycleError(reports []StepReport, children [][]int, left []int) error {
	names := make([]string, 0, len(left))

	for _, i := range left {
		if reach(children, i)[i] {
			names = append(names, fmt.Sprintf("%q", reports[i].Name))
		}
	}

	return fmt.Errorf("%w: steps %v form a cycle", ErrCycle, names)
}
