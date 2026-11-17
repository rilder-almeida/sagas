package sagas

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

var (
	// ErrInvalidState is returned when the state type or a state value cannot round-trip through its JSON encoding.
	ErrInvalidState = errors.New("sagas: invalid state")
	// ErrConflict is returned when a step changes a field that another step committed after the step's snapshot.
	ErrConflict = errors.New("sagas: state conflict")
)

type offense struct {
	path string
	rule string
}

type typeWalk struct {
	onPath map[reflect.Type]bool
	done   map[reflect.Type][]offense
	cut    bool
}

func checkState(t reflect.Type) error {
	name := t.Name()
	if name == "" {
		name = t.String()
	}

	if t.Kind() != reflect.Struct {
		return fmt.Errorf("%w: %s: not a struct", ErrInvalidState, name)
	}

	w := typeWalk{onPath: map[reflect.Type]bool{}, done: map[reflect.Type][]offense{}}
	offenses := w.walk(t)

	errs := make([]error, 0, len(offenses))
	for _, o := range offenses {
		errs = append(errs, fmt.Errorf("%w: %s%s: %s", ErrInvalidState, name, o.path, o.rule))
	}

	return errors.Join(errs...)
}

func encodeField(name string, v any) ([]byte, error) {
	data, err := json.Marshal(
		v,
		json.Deterministic(true),
		json.FormatNilSliceAsNull(true),
		json.FormatNilMapAsNull(true),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidState, name, err)
	}

	return data, nil
}

func decodeField(name string, data []byte, v any) error {
	dst, err := decodeTarget(name, v)
	if err != nil {
		return err
	}

	fresh := reflect.New(dst.Type().Elem())

	err = json.Unmarshal(data, fresh.Interface())
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidState, name, err)
	}

	dst.Elem().Set(fresh.Elem())

	return nil
}

func decodeTarget(name string, v any) (reflect.Value, error) {
	dst := reflect.ValueOf(v)
	if dst.Kind() != reflect.Pointer || dst.IsNil() {
		return reflect.Value{}, fmt.Errorf("%w: %s: decode destination %T is not a non-nil pointer",
			ErrInvalidState, name, v)
	}

	return dst, nil
}

func (w *typeWalk) walk(t reflect.Type) []offense {
	if w.onPath[t] {
		w.cut = true

		return nil
	}

	if offenses, found := w.done[t]; found {
		return offenses
	}

	outerCut := w.cut
	w.cut = false
	w.onPath[t] = true
	offenses := w.rules(t)
	delete(w.onPath, t)

	if !w.cut {
		w.done[t] = offenses
	}

	w.cut = w.cut || outerCut

	return offenses
}

func (w *typeWalk) rules(t reflect.Type) []offense {
	if t == reflect.TypeFor[time.Duration]() || t == reflect.TypeFor[jsontext.Value]() {
		return []offense{{rule: "rejected type " + t.String()}}
	}

	if method := definesEncoding(t); method != "" {
		return []offense{{rule: "defines its own encoding with " + method}}
	}

	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.String:
		return nil
	case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128, reflect.Chan, reflect.Func,
		reflect.Interface, reflect.UnsafePointer:
		return []offense{{rule: "rejected kind " + t.Kind().String()}}
	case reflect.Array, reflect.Slice:
		return under(nil, "[]", w.walk(t.Elem()))
	case reflect.Pointer:
		return w.walkPointer(t)
	case reflect.Map:
		return w.walkMap(t)
	case reflect.Struct:
		return w.walkStruct(t)
	}

	return nil
}

func (w *typeWalk) walkPointer(t reflect.Type) []offense {
	if kind := t.Elem().Kind(); kind == reflect.Pointer || kind == reflect.Slice || kind == reflect.Map {
		return []offense{{rule: "pointer to a pointer, slice or map"}}
	}

	return w.walk(t.Elem())
}

func (w *typeWalk) walkMap(t reflect.Type) []offense {
	var offenses []offense
	if isMapKeyKind(t.Key().Kind()) {
		offenses = under(offenses, "[key]", w.walk(t.Key()))
	} else {
		offenses = append(offenses, offense{path: "[key]", rule: "map key must be a string or an integer"})
	}

	return under(offenses, "[value]", w.walk(t.Elem()))
}

func (w *typeWalk) walkStruct(t reflect.Type) []offense {
	var offenses []offense

	for field := range t.Fields() {
		path := "." + field.Name

		switch {
		case field.Anonymous:
			offenses = append(offenses, offense{path: path, rule: "embedded field"})
		case !field.IsExported():
			offenses = append(offenses, offense{path: path, rule: "unexported field"})
		case field.Tag != "":
			offenses = append(offenses, offense{path: path, rule: "struct tag"})
		default:
			offenses = under(offenses, path, w.walk(field.Type))
		}
	}

	return offenses
}

func under(dst []offense, prefix string, offenses []offense) []offense {
	for _, o := range offenses {
		dst = append(dst, offense{path: prefix + o.path, rule: o.rule})
	}

	return dst
}

func definesEncoding(t reflect.Type) string {
	ptr := reflect.PointerTo(t)
	for _, method := range [...]string{
		"MarshalJSON", "UnmarshalJSON", "MarshalJSONTo", "UnmarshalJSONFrom",
		"MarshalText", "UnmarshalText", "AppendText",
	} {
		if _, found := ptr.MethodByName(method); found {
			return method
		}
	}

	return ""
}

func isMapKeyKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	case reflect.Invalid, reflect.Bool, reflect.Uintptr, reflect.Float32, reflect.Float64, reflect.Complex64,
		reflect.Complex128, reflect.Array, reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice, reflect.Struct, reflect.UnsafePointer:
		return false
	}

	return false
}

type snapshot[S any] struct {
	fields   [][]byte
	versions []uint64
}

type store[S any] struct {
	fields   [][]byte
	versions []uint64
	steps    []string
}

func newStore[S any](initial S) (*store[S], error) {
	if err := checkState(reflect.TypeFor[S]()); err != nil {
		return nil, err
	}

	fields, err := encodeFields(reflect.ValueOf(initial))
	if err != nil {
		return nil, err
	}

	return &store[S]{
		fields:   fields,
		versions: make([]uint64, len(fields)),
		steps:    make([]string, len(fields)),
	}, nil
}

func (s *store[S]) snapshot() snapshot[S] {
	return snapshot[S]{fields: slices.Clone(s.fields), versions: slices.Clone(s.versions)}
}

func (s *store[S]) commit(step string, snap snapshot[S], attempt S) ([]string, error) {
	v := reflect.ValueOf(attempt)

	fields, err := encodeFields(v)
	if err != nil {
		return nil, err
	}

	var (
		changed   []int
		conflicts []error
	)

	for i, data := range fields {
		switch {
		case bytes.Equal(data, snap.fields[i]):
		case s.versions[i] != snap.versions[i]:
			conflicts = append(conflicts, fmt.Errorf("%w: field %s changed by step %q after step %q committed it",
				ErrConflict, v.Type().Field(i).Name, step, s.steps[i]))
		default:
			changed = append(changed, i)
		}
	}

	if len(conflicts) > 0 {
		return nil, errors.Join(conflicts...)
	}

	names := make([]string, 0, len(changed))
	for _, i := range changed {
		s.fields[i] = fields[i]
		s.versions[i]++
		s.steps[i] = step
		names = append(names, v.Type().Field(i).Name)
	}

	return names, nil
}

func (s *store[S]) writeBack(dst *S) error {
	if dst == nil {
		return fmt.Errorf("%w: write-back destination is nil", ErrInvalidState)
	}

	out, err := copyState(snapshot[S]{fields: s.fields})
	if err != nil {
		return err
	}

	*dst = *out

	return nil
}

func copyState[S any](snap snapshot[S]) (*S, error) {
	out := new(S)

	var errs []error

	v := reflect.ValueOf(out).Elem()
	for i, data := range snap.fields {
		if err := decodeField(v.Type().Field(i).Name, data, v.Field(i).Addr().Interface()); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return out, nil
}

func encodeFields(v reflect.Value) ([][]byte, error) {
	fields := make([][]byte, v.NumField())

	var errs []error

	for i := range fields {
		data, err := encodeField(v.Type().Field(i).Name, v.Field(i).Interface())
		if err != nil {
			errs = append(errs, err)
		}

		fields[i] = data
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return fields, nil
}
