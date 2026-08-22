package schema

import (
	"encoding/json"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

func obj(t *testing.T, s string) map[string]any {
	return decode(t, s).(map[string]any)
}

func TestValidateOK(t *testing.T) {
	sch := obj(t, `{"type":"object","required":["n"],"properties":{"n":{"type":"integer"}}}`)
	if errs := Validate(sch, decode(t, `{"n":5}`)); len(errs) != 0 {
		t.Fatalf("expected valid, got %v", errs)
	}
}

func TestMissingRequired(t *testing.T) {
	sch := obj(t, `{"type":"object","required":["n"]}`)
	if errs := Validate(sch, decode(t, `{}`)); len(errs) == 0 {
		t.Fatal("expected missing-required error")
	}
}

func TestTypeMismatch(t *testing.T) {
	sch := obj(t, `{"type":"object","properties":{"n":{"type":"integer"}}}`)
	if errs := Validate(sch, decode(t, `{"n":"hi"}`)); len(errs) == 0 {
		t.Fatal("expected type error for n")
	}
}

func TestArrayItemsAndEnum(t *testing.T) {
	sch := obj(t, `{"type":"array","items":{"type":"string","enum":["a","b"]}}`)
	if errs := Validate(sch, decode(t, `["a","b"]`)); len(errs) != 0 {
		t.Fatalf("expected valid, got %v", errs)
	}
	if errs := Validate(sch, decode(t, `["a","z"]`)); len(errs) == 0 {
		t.Fatal("expected enum violation")
	}
}

func TestAdditionalPropertiesFalse(t *testing.T) {
	sch := obj(t, `{"type":"object","properties":{"a":{}},"additionalProperties":false}`)
	if errs := Validate(sch, decode(t, `{"a":1,"b":2}`)); len(errs) == 0 {
		t.Fatal("expected additional-property error")
	}
}

func TestEmptySchemaAcceptsAnything(t *testing.T) {
	if errs := Validate(nil, decode(t, `{"whatever":true}`)); len(errs) != 0 {
		t.Fatalf("nil schema should accept anything, got %v", errs)
	}
}
