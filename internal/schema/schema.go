// Package schema implements a small, dependency-free subset of JSON Schema,
// sufficient to validate node outputs against user-declared output_schema.
//
// Supported keywords: type, properties, required, additionalProperties, items,
// enum, minItems, maxItems. Unknown keywords are ignored (lenient), so richer
// schemas still validate on the parts we understand.
package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Validate checks data against the given schema (a decoded JSON Schema object).
// It returns a list of human-readable, path-qualified errors; empty means valid.
// A nil or empty schema accepts anything.
func Validate(sch map[string]any, data any) []error {
	if len(sch) == 0 {
		return nil
	}
	var errs []error
	validate("$", sch, data, &errs)
	return errs
}

func validate(path string, sch map[string]any, data any, errs *[]error) {
	if t, ok := sch["type"]; ok {
		if !typeMatches(t, data) {
			*errs = append(*errs, fmt.Errorf("%s: expected type %v, got %s", path, t, jsonType(data)))
			return // further keyword checks assume the base type matched
		}
	}

	if enum, ok := sch["enum"].([]any); ok {
		if !enumContains(enum, data) {
			*errs = append(*errs, fmt.Errorf("%s: value %v not in enum", path, data))
		}
	}

	switch d := data.(type) {
	case map[string]any:
		validateObject(path, sch, d, errs)
	case []any:
		validateArray(path, sch, d, errs)
	}
}

func validateObject(path string, sch map[string]any, obj map[string]any, errs *[]error) {
	props, _ := sch["properties"].(map[string]any)

	// required
	for _, r := range toStringSlice(sch["required"]) {
		if _, ok := obj[r]; !ok {
			*errs = append(*errs, fmt.Errorf("%s: missing required property %q", path, r))
		}
	}

	// declared properties
	for name, child := range obj {
		if psch, ok := props[name].(map[string]any); ok {
			validate(path+"."+name, psch, child, errs)
		}
	}

	// additionalProperties: false rejects undeclared keys
	if ap, ok := sch["additionalProperties"]; ok {
		if allowed, isBool := ap.(bool); isBool && !allowed {
			for name := range obj {
				if _, declared := props[name]; !declared {
					*errs = append(*errs, fmt.Errorf("%s: additional property %q not allowed", path, name))
				}
			}
		}
	}
}

func validateArray(path string, sch map[string]any, arr []any, errs *[]error) {
	if mi, ok := toInt(sch["minItems"]); ok && len(arr) < mi {
		*errs = append(*errs, fmt.Errorf("%s: expected at least %d items, got %d", path, mi, len(arr)))
	}
	if ma, ok := toInt(sch["maxItems"]); ok && len(arr) > ma {
		*errs = append(*errs, fmt.Errorf("%s: expected at most %d items, got %d", path, ma, len(arr)))
	}
	if items, ok := sch["items"].(map[string]any); ok {
		for i, el := range arr {
			validate(fmt.Sprintf("%s[%d]", path, i), items, el, errs)
		}
	}
}

func typeMatches(t any, data any) bool {
	switch tt := t.(type) {
	case string:
		return oneType(tt, data)
	case []any: // union of types
		for _, x := range tt {
			if s, ok := x.(string); ok && oneType(s, data) {
				return true
			}
		}
		return false
	}
	return true // unrecognized type spec: don't block
}

func oneType(t string, data any) bool {
	switch t {
	case "object":
		_, ok := data.(map[string]any)
		return ok
	case "array":
		_, ok := data.([]any)
		return ok
	case "string":
		_, ok := data.(string)
		return ok
	case "boolean":
		_, ok := data.(bool)
		return ok
	case "number":
		return isNumber(data)
	case "integer":
		return isInteger(data)
	case "null":
		return data == nil
	}
	return true
}

func isNumber(d any) bool {
	switch d.(type) {
	case float64, float32, int, int64, json.Number:
		return true
	}
	return false
}

func isInteger(d any) bool {
	switch v := d.(type) {
	case int, int64:
		return true
	case float64:
		return v == math.Trunc(v)
	case float32:
		return float64(v) == math.Trunc(float64(v))
	}
	return false
}

func jsonType(d any) string {
	switch d.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		if isInteger(d) {
			return "integer"
		}
		if isNumber(d) {
			return "number"
		}
		return "unknown"
	}
}

func enumContains(enum []any, data any) bool {
	want, _ := json.Marshal(data)
	for _, e := range enum {
		got, _ := json.Marshal(e)
		if string(got) == string(want) {
			return true
		}
	}
	return false
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out) // stable error ordering
	return out
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
