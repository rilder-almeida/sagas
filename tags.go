package sagas

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

const (
	tagKey    = "sagas"
	tagID     = "id"
	tagSecret = "secret"
)

func stateTags(t reflect.Type) (int, []error) {
	var errs []error

	id := -1
	name := typeLabel(t)

	for i := range t.NumField() {
		field := t.Field(i)

		tag := fieldTagRules(&field)
		if tag.id && id >= 0 {
			tag.rules = append(tag.rules, "id is already on field "+t.Field(id).Name)
		}

		if tag.id && id < 0 {
			id = i
		}

		for _, rule := range tag.rules {
			errs = append(errs, fmt.Errorf("%w: %s.%s: %s", ErrInvalidState, name, field.Name, rule))
		}
	}

	if len(errs) > 0 {
		return -1, errs
	}

	return id, nil
}

type fieldTag struct {
	id    bool
	rules []string
}

func fieldTagRules(field *reflect.StructField) fieldTag {
	tag, ok := field.Tag.Lookup(tagKey)
	if !ok || field.Anonymous || !field.IsExported() {
		return fieldTag{}
	}

	var rules []string

	values := strings.Split(tag, ",")
	for _, v := range values {
		if v != tagID && v != tagSecret {
			rules = append(rules, fmt.Sprintf("sagas tag value %q is unknown", v))
		}
	}

	if !slices.Contains(values, tagID) {
		return fieldTag{rules: rules}
	}

	if slices.Contains(values, tagSecret) {
		rules = append(rules, "id and secret cannot share a field")
	}

	if field.Type.Kind() != reflect.String {
		rules = append(rules, fmt.Sprintf("id needs a string field, got %s", field.Type.Kind()))
	}

	return fieldTag{id: true, rules: rules}
}

func tagKeys(tag reflect.StructTag) []string {
	var keys []string

	rest := strings.TrimSpace(string(tag))
	for rest != "" {
		key, after, found := strings.Cut(rest, ":")
		if !found || key == "" || strings.ContainsAny(key, " \t\"") {
			return append(keys, "?")
		}

		quoted, err := strconv.QuotedPrefix(after)
		if err != nil {
			return append(keys, "?")
		}

		keys = append(keys, key)
		rest = strings.TrimSpace(after[len(quoted):])
	}

	return keys
}

func nestedTagOffenses(t reflect.Type) []offense {
	var offenses []offense

	for field := range t.Fields() {
		if _, ok := field.Tag.Lookup(tagKey); ok && !field.Anonymous && field.IsExported() {
			offenses = append(offenses, offense{path: "." + field.Name, rule: "sagas tag below the top level"})
		}
	}

	return offenses
}

func tagOffense(path string, tag reflect.StructTag, top bool) []offense {
	var offenses []offense

	for _, key := range tagKeys(tag) {
		switch {
		case key == tagKey && top:
		case key == tagKey:
			offenses = append(offenses, offense{path: path, rule: "sagas tag below the top level"})
		default:
			offenses = append(offenses, offense{path: path, rule: "struct tag"})

			return offenses
		}
	}

	return offenses
}

func refuseExecutionIDMethod[S any]() error {
	if _, ok := any((*S)(nil)).(interface{ ExecutionID() string }); !ok {
		return nil
	}

	return fmt.Errorf("%w: %s: method ExecutionID is no longer read, tag the string field sagas:\"id\" instead",
		ErrInvalidState, typeLabel(reflect.TypeFor[S]()))
}
