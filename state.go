package sagas

import (
	"encoding"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
)

var (
	// ErrInvalidState is returned when the state type or a state value cannot round-trip through its JSON encoding.
	ErrInvalidState = errors.New("sagas: invalid state")
	// ErrConflict is returned when a step changes a field that another step committed after the step's snapshot.
	ErrConflict = errors.New("sagas: state conflict")
)

func checkState(t reflect.Type) error {
	name := t.Name()
	if name == "" {
		name = t.String()
	}

	if t.Kind() != reflect.Struct {
		return fmt.Errorf("%w: %s: not a struct", ErrInvalidState, name)
	}

	return errors.Join(walkType(t, name, map[reflect.Type]bool{})...)
}

func encodeField(v any) ([]byte, error) {
	data, err := json.Marshal(
		v,
		json.Deterministic(true),
		json.FormatNilSliceAsNull(true),
		json.FormatNilMapAsNull(true),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidState, err)
	}

	return data, nil
}

func decodeField(data []byte, v any) error {
	err := json.Unmarshal(
		data,
		v,
		json.Deterministic(true),
		json.FormatNilSliceAsNull(true),
		json.FormatNilMapAsNull(true),
	)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidState, err)
	}

	return nil
}

func walkType(t reflect.Type, path string, onPath map[reflect.Type]bool) []error {
	if onPath[t] || hasJSONMethods(t) {
		return nil
	}

	onPath[t] = true
	defer delete(onPath, t)

	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.String:
		return nil
	case reflect.Invalid, reflect.Complex64, reflect.Complex128, reflect.Chan, reflect.Func,
		reflect.Interface, reflect.UnsafePointer:
		return []error{invalidAt(path, "rejected kind "+t.Kind().String())}
	case reflect.Array, reflect.Slice:
		return walkType(t.Elem(), path+"[]", onPath)
	case reflect.Pointer:
		return walkType(t.Elem(), path, onPath)
	case reflect.Map:
		return walkMap(t, path, onPath)
	case reflect.Struct:
		return walkStruct(t, path, onPath)
	}

	return nil
}

func walkMap(t reflect.Type, path string, onPath map[reflect.Type]bool) []error {
	var errs []error
	if !isMapKey(t.Key()) {
		errs = append(errs, invalidAt(path+"[key]", "map key cannot be a JSON object name"))
	}

	return append(errs, walkType(t.Elem(), path+"[value]", onPath)...)
}

func walkStruct(t reflect.Type, path string, onPath map[reflect.Type]bool) []error {
	var errs []error

	for field := range t.Fields() {
		fieldPath := path + "." + field.Name

		if !field.IsExported() {
			errs = append(errs, invalidAt(fieldPath, "unexported field"))

			continue
		}

		if _, tagged := field.Tag.Lookup("json"); tagged {
			errs = append(errs, invalidAt(fieldPath, "json tag"))

			continue
		}

		errs = append(errs, walkType(field.Type, fieldPath, onPath)...)
	}

	return errs
}

func invalidAt(path, rule string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidState, path, rule)
}

func hasJSONMethods(t reflect.Type) bool {
	ptr := reflect.PointerTo(t)
	marshals := ptr.Implements(reflect.TypeFor[json.Marshaler]()) ||
		ptr.Implements(reflect.TypeFor[json.MarshalerTo]())
	unmarshals := ptr.Implements(reflect.TypeFor[json.Unmarshaler]()) ||
		ptr.Implements(reflect.TypeFor[json.UnmarshalerFrom]())

	return marshals && unmarshals
}

func isMapKey(t reflect.Type) bool {
	if t.Implements(reflect.TypeFor[encoding.TextMarshaler]()) &&
		reflect.PointerTo(t).Implements(reflect.TypeFor[encoding.TextUnmarshaler]()) {
		return true
	}

	switch t.Kind() {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Invalid, reflect.Bool, reflect.Complex64, reflect.Complex128, reflect.Array, reflect.Chan,
		reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.Struct,
		reflect.UnsafePointer:
		return false
	}

	return false
}
