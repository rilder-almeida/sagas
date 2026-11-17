package sagas

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"
)

var (
	// ErrInvalidState is returned when the state type or a state value cannot round-trip through its JSON encoding.
	ErrInvalidState = errors.New("sagas: invalid state")
	// ErrConflict is returned when a step changes a field that another step committed after the step's snapshot.
	ErrConflict = errors.New("sagas: state conflict")
)

type typeWalk struct {
	onPath  map[reflect.Type]bool
	modules []string
}

func checkState(t reflect.Type) error {
	name := t.Name()
	if name == "" {
		name = t.String()
	}

	if t.Kind() != reflect.Struct {
		return fmt.Errorf("%w: %s: not a struct", ErrInvalidState, name)
	}

	w := typeWalk{onPath: map[reflect.Type]bool{}, modules: modulePaths()}

	return errors.Join(w.walk(t, name)...)
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

func (w *typeWalk) walk(t reflect.Type, path string) []error {
	if w.onPath[t] {
		return nil
	}

	if t.PkgPath() != "" && isStandardPath(t.PkgPath(), w.modules) {
		return []error{invalidAt(path, "standard library type "+t.String())}
	}

	if method := definesEncoding(t); method != "" {
		return []error{invalidAt(path, "defines its own encoding with "+method)}
	}

	w.onPath[t] = true
	defer delete(w.onPath, t)

	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.String:
		return nil
	case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128, reflect.Chan, reflect.Func,
		reflect.Interface, reflect.UnsafePointer:
		return []error{invalidAt(path, "rejected kind "+t.Kind().String())}
	case reflect.Array, reflect.Slice:
		return w.walk(t.Elem(), path+"[]")
	case reflect.Pointer:
		return w.walk(t.Elem(), path)
	case reflect.Map:
		return w.walkMap(t, path)
	case reflect.Struct:
		return w.walkStruct(t, path)
	}

	return nil
}

func (w *typeWalk) walkMap(t reflect.Type, path string) []error {
	var errs []error
	if isMapKeyKind(t.Key().Kind()) {
		errs = w.walk(t.Key(), path+"[key]")
	} else {
		errs = []error{invalidAt(path+"[key]", "map key must be a string or an integer")}
	}

	return append(errs, w.walk(t.Elem(), path+"[value]")...)
}

func (w *typeWalk) walkStruct(t reflect.Type, path string) []error {
	var errs []error

	for field := range t.Fields() {
		fieldPath := path + "." + field.Name

		switch {
		case field.Anonymous:
			errs = append(errs, invalidAt(fieldPath, "embedded field"))
		case !field.IsExported():
			errs = append(errs, invalidAt(fieldPath, "unexported field"))
		case field.Tag != "":
			errs = append(errs, invalidAt(fieldPath, "struct tag"))
		default:
			errs = append(errs, w.walk(field.Type, fieldPath)...)
		}
	}

	return errs
}

func invalidAt(path, rule string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidState, path, rule)
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

func modulePaths() []string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}

	paths := make([]string, 0, len(info.Deps)+1)
	paths = append(paths, info.Main.Path)

	for _, dep := range info.Deps {
		paths = append(paths, dep.Path)
	}

	return paths
}

func isStandardPath(pkgPath string, modules []string) bool {
	if pkgPath == "" || pkgPath == "main" {
		return false
	}

	if len(modules) == 0 {
		first, _, _ := strings.Cut(pkgPath, "/")

		return !strings.Contains(first, ".")
	}

	for _, module := range modules {
		if pkgPath == module || strings.HasPrefix(pkgPath, module+"/") {
			return false
		}
	}

	return true
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
