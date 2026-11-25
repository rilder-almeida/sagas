package sagas

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// ErrInvalidState is returned when the state type breaks a state rule or a state value cannot round-trip through its
// JSON encoding.
var ErrInvalidState = &KindError{name: "invalid-state", message: "sagas: invalid state"}

type offense struct {
	path string
	rule string
}

type typeWalk struct {
	root   reflect.Type
	onPath map[reflect.Type]bool
	done   map[reflect.Type][]offense
	cut    bool
}

func typeLabel(t reflect.Type) string {
	if name := t.Name(); name != "" {
		return name
	}

	return t.String()
}

func examineState(t reflect.Type) (int, error) {
	name := typeLabel(t)

	if t.Kind() != reflect.Struct {
		return -1, fmt.Errorf("%w: %s: not a struct", ErrInvalidState, name)
	}

	w := typeWalk{root: t, onPath: map[reflect.Type]bool{}, done: map[reflect.Type][]offense{}}
	offenses := w.walk(t)

	idField, tagErrs := stateTags(t)

	errs := make([]error, 0, len(offenses)+len(tagErrs))
	for _, o := range offenses {
		errs = append(errs, fmt.Errorf("%w: %s%s: %s", ErrInvalidState, name, o.path, o.rule))
	}

	return idField, errors.Join(append(errs, tagErrs...)...)
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

func hideSecret(field *reflect.StructField, verb string, err error) error {
	if err == nil || !fieldTagRules(field).secret {
		return err
	}

	return fmt.Errorf("%w: %s: secret value cannot be %s", ErrInvalidState, field.Name, verb)
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

		if t == w.root {
			return nestedTagOffenses(t)
		}

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
	if t == reflect.TypeFor[time.Duration]() || t == reflect.TypeFor[jsontext.Value]() || t == reflect.TypeFor[None]() {
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

	top := t == w.root

	for field := range t.Fields() {
		path := "." + field.Name

		switch {
		case field.Anonymous:
			offenses = append(offenses, offense{path: path, rule: "embedded field"})
		case !field.IsExported():
			offenses = append(offenses, offense{path: path, rule: "unexported field"})
		default:
			tagged := tagOffense(path, field.Tag, top)
			if len(tagged) > 0 {
				offenses = append(offenses, tagged...)

				continue
			}

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
	fields [][]byte
}

type store[S any] struct {
	initial   [][]byte
	outputs   [][]byte
	field     []int
	order     []int
	ancestors [][]bool
	names     []string
	secret    []bool
}

func newStore[S any](initial [][]byte, field, order []int, ancestors [][]bool) *store[S] {
	t := reflect.TypeFor[S]()

	names := make([]string, len(initial))
	secret := make([]bool, len(initial))

	for i := range names {
		f := t.Field(i)
		names[i], secret[i] = f.Name, fieldTagRules(&f).secret
	}

	return &store[S]{
		initial: initial, outputs: make([][]byte, len(field)), field: field, order: order, ancestors: ancestors,
		names: names, secret: secret,
	}
}

func (s *store[S]) base(step int) snapshot[S] {
	fields := slices.Clone(s.initial)

	for _, j := range s.order {
		if s.ancestors[step][j] && s.outputs[j] != nil {
			fields[s.field[j]] = s.outputs[j]
		}
	}

	return snapshot[S]{fields: fields}
}

func (s *store[S]) compensationBase(step int) snapshot[S] {
	snap := s.base(step)
	if s.outputs[step] != nil {
		snap.fields[s.field[step]] = s.outputs[step]
	}

	return snap
}

type commitResult struct {
	values  []FieldValue
	secrets []FieldValue
	label   *KindError
}

func (s *store[S]) put(step int, out encodedOutput) (commitResult, error) {
	f := s.field[step]

	if out.err != nil {
		err := out.err

		if f >= 0 {
			field := reflect.TypeFor[S]().Field(f)
			err = hideSecret(&field, "encoded", err)
		}

		return commitResult{label: ErrInvalidState}, err
	}

	if f < 0 || out.data == nil {
		return commitResult{}, nil
	}

	s.outputs[step] = out.data
	value := FieldValue{Field: s.names[f], Value: out.data}

	if s.secret[f] {
		return commitResult{values: []FieldValue{{Field: value.Field}}, secrets: []FieldValue{value}}, nil
	}

	return commitResult{values: []FieldValue{value}}, nil
}

func (s *store[S]) restore(step int) {
	s.outputs[step] = nil
}

func (s *store[S]) initialValues() commitResult {
	res := commitResult{values: make([]FieldValue, len(s.initial))}

	for i, data := range s.initial {
		res.values[i] = FieldValue{Field: s.names[i], Value: data}
		if s.secret[i] {
			res.secrets = append(res.secrets, res.values[i])
			res.values[i].Value = nil
		}
	}

	return res
}

func (s *store[S]) writeBack(dst *S) error {
	if dst == nil {
		return fmt.Errorf("%w: write-back destination is nil", ErrInvalidState)
	}

	fields := slices.Clone(s.initial)

	for _, j := range s.order {
		if s.outputs[j] != nil {
			fields[s.field[j]] = s.outputs[j]
		}
	}

	out, err := copyState(snapshot[S]{fields: fields})
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
		field := v.Type().Field(i)
		if err := decodeField(field.Name, data, v.Field(i).Addr().Interface()); err != nil {
			errs = append(errs, hideSecret(&field, "decoded", err))
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
		field := v.Type().Field(i)

		data, err := encodeField(field.Name, v.Field(i).Interface())
		if err != nil {
			errs = append(errs, hideSecret(&field, "encoded", err))
		}

		fields[i] = data
	}

	return fields, errors.Join(errs...)
}

type fieldWrite struct {
	step string
	data []byte
}

type rebuild struct {
	index   map[string]int
	secret  []bool
	writes  [][]fieldWrite
	secrets SecretStore
}

func rebuildState[S any](ctx context.Context, events []Event, secrets SecretStore, through int) (*S, error) {
	t := reflect.TypeFor[S]()
	b := rebuild{
		index: make(map[string]int, t.NumField()), secret: make([]bool, t.NumField()),
		writes: make([][]fieldWrite, t.NumField()), secrets: secrets,
	}

	for i := range t.NumField() {
		field := t.Field(i)
		b.index[field.Name], b.secret[i] = i, fieldTagRules(&field).secret
	}

	for i := range events {
		if events[i].Seq > through {
			break
		}

		if err := b.apply(ctx, &events[i]); err != nil {
			return nil, err
		}
	}

	fields := make([][]byte, len(b.writes))
	for f, w := range b.writes {
		if len(w) > 0 {
			fields[f] = w[len(w)-1].data
		}
	}

	return copyState(snapshot[S]{fields: fields})
}

func (b *rebuild) apply(ctx context.Context, e *Event) error {
	own := func(w fieldWrite) bool { return w.step == e.Step }

	if e.Kind == EventCompensationSucceeded && len(e.Values) == 0 {
		for f := range b.writes {
			b.writes[f] = slices.DeleteFunc(b.writes[f], own)
		}
	}

	for _, v := range e.Values {
		f, found := b.index[v.Field]
		if !found {
			return fmt.Errorf("%w: seq %d: unknown field %s", ErrInvalidState, e.Seq, v.Field)
		}

		data, err := b.value(ctx, e, f, &v)
		if err != nil {
			return err
		}

		if i := slices.IndexFunc(b.writes[f], own); e.Kind == EventCompensationSucceeded && i >= 0 {
			b.writes[f][i].data = data
		} else {
			b.writes[f] = append(b.writes[f], fieldWrite{step: e.Step, data: data})
		}
	}

	return nil
}

func (b *rebuild) value(ctx context.Context, e *Event, f int, v *FieldValue) ([]byte, error) {
	if !b.secret[f] {
		return v.Value, nil
	}

	return b.secrets.Get(ctx, secretKeyOf(e, v.Field))
}
