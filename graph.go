package sagas

import (
	"fmt"
	"slices"
)

func indexSteps(defs []stepDef) (map[string]int, []error) {
	index := make(map[string]int, len(defs))

	var errs []error

	for i := range defs {
		if _, seen := index[defs[i].name]; seen {
			errs = append(errs, fmt.Errorf("%w: step %q is declared more than once", ErrDuplicateStep, defs[i].name))

			continue
		}

		index[defs[i].name] = i
	}

	return index, errs
}

func link(defs []stepDef, index map[string]int) ([][]int, []error) {
	parents := make([][]int, len(defs))

	var errs []error

	for i := range defs {
		parents[i] = []int{}

		for _, name := range defs[i].after {
			p, ok := index[name]
			if !ok {
				errs = append(errs, fmt.Errorf("%w: step %q depends on %q", ErrUnknownDependency, defs[i].name, name))

				continue
			}

			if !slices.Contains(parents[i], p) {
				parents[i] = append(parents[i], p)
			}
		}
	}

	return parents, errs
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

func topoOrder(parents [][]int) ordering {
	children := childrenOf(parents)
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

func zones(parents, children [][]int, pivot int) []Zone {
	out := make([]Zone, len(parents))
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

func pivotsOf(defs []stepDef) []int {
	var pivots []int

	for i := range defs {
		if defs[i].pivot {
			pivots = append(pivots, i)
		}
	}

	return pivots
}

func checkPivot(saga string, defs []stepDef) []error {
	pivots := pivotsOf(defs)
	if len(pivots) <= 1 {
		return nil
	}

	names := make([]string, 0, len(pivots))
	for _, p := range pivots {
		names = append(names, fmt.Sprintf("%q", defs[p].name))
	}

	return []error{fmt.Errorf("%w: saga %q: more than one Pivot, on steps %v", ErrInvalidStep, saga, names)}
}

func checkReach(defs []stepDef, parents [][]int) []error {
	pivots := pivotsOf(defs)
	if len(pivots) != 1 {
		return nil
	}

	pivot := pivots[0]
	before := reach(parents, pivot)
	after := reach(childrenOf(parents), pivot)

	var errs []error

	for i := range defs {
		switch {
		case i != pivot && !before[i] && !after[i]:
			errs = append(errs, fmt.Errorf("%w: step %q: neither before nor after the pivot %q",
				ErrInvalidStep, defs[i].name, defs[pivot].name))
		case after[i] && defs[i].undo != nil:
			errs = append(errs, fmt.Errorf("%w: step %q: a step after the pivot has Compensate",
				ErrInvalidStep, defs[i].name))
		default:
		}
	}

	return errs
}

type graph struct {
	parents [][]int
	order   []int
}

func checkGraph(defs []stepDef) (graph, []error) {
	index, errs := indexSteps(defs)
	parents, linkErrs := link(defs, index)
	errs = append(errs, linkErrs...)

	o := topoOrder(parents)
	if len(o.cycle) > 0 {
		names := make([]string, 0, len(o.cycle))
		for _, i := range o.cycle {
			names = append(names, fmt.Sprintf("%q", defs[i].name))
		}

		errs = append(errs, fmt.Errorf("%w: steps %v depend on each other", ErrCycle, names))
	}

	return graph{parents: parents, order: o.order}, errs
}
