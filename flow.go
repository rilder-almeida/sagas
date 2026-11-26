package sagas

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Flow is a step, or a group of steps that Sequence or Parallel builds, where one pointer is one step wherever it
// appears. A step reads the outputs of the steps it waits for, directly or not, and never the output of a step that
// runs at the same time.
type Flow interface {
	flow()
}

// Group is a Flow of several items that Sequence or Parallel builds.
type Group struct {
	kind  string
	items []Flow
}

func (*Group) flow() {}

// Sequence runs items one after another, and a later output of one type replaces an earlier one.
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
	flows    []binder
	settings []stepSettings
	parents  [][]int
	index    map[Flow]int
	errs     []error
}

func resolve(flow Flow) resolution {
	r := resolution{index: map[Flow]int{}}

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

	return r.group(g)
}

func (r *resolution) group(g *Group) ends {
	var out ends

	for i, item := range g.items {
		at := fmt.Sprintf("%s item at index %d", g.kind, i)
		if sub, isGroup := item.(*Group); isGroup && sub != nil && len(sub.items) == 0 {
			r.errs = append(r.errs, fmt.Errorf("%w: %s is an empty %s", ErrInvalidStep, at, sub.kind))

			continue
		}

		e := r.visit(item, at)

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
	b, ok := f.(binder)

	embed, embeds := embedsStep(reflect.TypeOf(f))
	if !ok || !embeds {
		r.errs = append(r.errs, fmt.Errorf("%w: %s has type %s, want a pointer to a struct that embeds Step or Pivot "+
			"by value", ErrInvalidStep, at, reflect.TypeOf(f)))

		return ends{}
	}

	i, seen := r.index[f]
	if !seen {
		i = len(r.flows)
		r.index[f] = i
		r.flows = append(r.flows, b)
		r.settings = append(r.settings, settingsOf(f, &embed))
		r.parents = append(r.parents, []int{})
	}

	return ends{heads: []int{i}, tails: []int{i}}
}

func embedsStep(t reflect.Type) (reflect.StructField, bool) {
	if t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}

	seen := map[reflect.Type]bool{}

	for level := []embedPath{{field: reflect.StructField{Type: t.Elem()}}}; len(level) > 0; {
		scan := scanEmbeds(level, seen)
		if len(scan.found) > 0 {
			if len(scan.found) != 1 || scan.found[0].pointer {
				return reflect.StructField{}, false
			}

			return scan.found[0].field, true
		}

		level = scan.next
	}

	return reflect.StructField{}, false
}

type embedPath struct {
	field   reflect.StructField
	pointer bool
}

type embedScan struct {
	found []embedPath
	next  []embedPath
}

func scanEmbeds(level []embedPath, seen map[reflect.Type]bool) embedScan {
	var scan embedScan

	level = slices.DeleteFunc(slices.Clone(level), func(p embedPath) bool { return seen[p.field.Type] })
	for _, parent := range level {
		seen[parent.field.Type] = true
	}

	for _, parent := range level {
		for f := range parent.field.Type.Fields() {
			child, ok := embedChild(&parent, &f)

			switch {
			case !ok:
			case libraryEmbed(child.field.Type) != "":
				scan.found = append(scan.found, child)
			default:
				scan.next = append(scan.next, child)
			}
		}
	}

	return scan
}

func embedChild(parent *embedPath, f *reflect.StructField) (embedPath, bool) {
	child := *f

	pointer := child.Type.Kind() == reflect.Pointer
	if pointer {
		child.Type = child.Type.Elem()
	}

	if !child.Anonymous || child.Type.Kind() != reflect.Struct {
		return embedPath{}, false
	}

	child.Index = append(slices.Clone(parent.field.Index), child.Index...)

	return embedPath{field: child, pointer: parent.pointer || pointer}, true
}

func libraryEmbed(t reflect.Type) string {
	base, args, generic := strings.Cut(t.Name(), "[")
	if !generic || args == "" || t.PkgPath() != reflect.TypeFor[None]().PkgPath() {
		return ""
	}

	if base != "Step" && base != "Pivot" {
		return ""
	}

	return base
}

func settingsOf(f Flow, embed *reflect.StructField) stepSettings {
	v := reflect.ValueOf(f).Elem().FieldByIndex(embed.Index)
	pivot := libraryEmbed(embed.Type) == "Pivot"

	retry, ok := reflect.TypeAssert[RetryPolicy](v.FieldByName("RetryPolicy"))
	if !ok {
		panic(fmt.Sprintf("sagas: field RetryPolicy of %s is not a RetryPolicy", v.Type()))
	}

	s := stepSettings{
		name: v.FieldByName("Name").String(), description: v.FieldByName("Description").String(), retry: retry,
		timeout: time.Duration(v.FieldByName("AttemptTimeout").Int()), pivot: pivot,
	}

	if !pivot {
		s.failure = FailurePolicy(v.FieldByName("FailurePolicy").String())
		s.recovery, ok = reflect.TypeAssert[RecoveryPolicy](v.FieldByName("RecoveryPolicy"))

		if !ok {
			panic(fmt.Sprintf("sagas: field RecoveryPolicy of %s is not a RecoveryPolicy", v.Type()))
		}
	}

	return s
}

func shortType(t reflect.Type) string {
	var b strings.Builder

	s, start := t.String(), 0

	for i := 0; i <= len(s); i++ {
		if i < len(s) && !strings.ContainsRune("[], *", rune(s[i])) {
			continue
		}

		token := s[start:i]
		if j := strings.LastIndexByte(token, '/'); j >= 0 {
			token = token[j+1:]
		}

		b.WriteString(token)

		if i < len(s) {
			b.WriteByte(s[i])

			if s[i] == ',' {
				b.WriteByte(' ')
			}
		}

		start = i + 1
	}

	return b.String()
}

func (r *resolution) link(child int, parents []int) {
	for _, p := range parents {
		if !slices.Contains(r.parents[child], p) {
			r.parents[child] = append(r.parents[child], p)
		}
	}
}
