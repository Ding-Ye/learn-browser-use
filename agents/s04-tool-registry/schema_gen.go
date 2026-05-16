package main

import (
	"encoding/json"
	"reflect"
	"strings"
)

// SchemaFromStruct walks a Go struct value via reflection and produces
// a JSON Schema describing it. We use it so each Tool only has to
// declare its argument struct once — the schema falls out for free.
//
// Supported field shapes:
//
//   - string                                 → {"type":"string"}
//   - int, int32, int64                      → {"type":"integer"}
//   - bool                                   → {"type":"boolean"}
//   - float32, float64                       → {"type":"number"}
//   - slice of any of the above              → {"type":"array","items":{...}}
//   - nested struct                          → {"type":"object","properties":...}
//
// Tags we honour:
//
//   - `json:"foo"`        — field name in the schema (otherwise lowercase first-letter)
//   - `json:"foo,omitempty"` — same as above; the omitempty hint is ignored for schema
//   - `desc:"..."`        — copied into the field's "description" key
//
// All non-`omitempty` fields are listed in `required`. This is a
// deliberate simplification: upstream's pydantic model lets a field
// have an explicit default which removes it from required. Our zero
// values are not distinguishable from "field not set", so we treat
// `omitempty` as the "optional" marker.
//
// The function takes interface{} (a zero value of the target struct)
// rather than reflect.Type because reflect.TypeOf is awkward to spell
// from main and from tests.
func SchemaFromStruct(zero interface{}) json.RawMessage {
	t := reflect.TypeOf(zero)
	if t == nil {
		return mustJSON(emptyObject())
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return mustJSON(buildObjectSchema(t))
}

// buildObjectSchema produces the JSON Schema for a struct type.
// We keep field iteration deterministic (in declaration order) so
// pretty-printed output is reproducible across runs.
func buildObjectSchema(t reflect.Type) map[string]interface{} {
	if t.Kind() != reflect.Struct {
		// Non-struct top level — surface a single-scalar schema. Real
		// tools always use structs, but this keeps the helper total.
		return scalarSchema(t)
	}

	props := make(map[string]interface{})
	// JSON object key order is officially unspecified, but most
	// encoders preserve insertion order for maps with string keys
	// in deterministic ways via marshalling; encoding/json sorts
	// keys alphabetically, which is fine for our purposes.
	var required []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, omitempty := jsonFieldName(f)
		if name == "-" {
			continue
		}

		fieldSchema := buildFieldSchema(f.Type)
		if desc, ok := f.Tag.Lookup("desc"); ok && desc != "" {
			fieldSchema["description"] = desc
		}
		props[name] = fieldSchema

		if !omitempty {
			required = append(required, name)
		}
	}

	out := map[string]interface{}{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// buildFieldSchema dispatches by Kind. We hit Ptr/Slice/Struct cases
// explicitly because they need recursion; scalars share scalarSchema.
func buildFieldSchema(t reflect.Type) map[string]interface{} {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return buildObjectSchema(t)
	case reflect.Slice, reflect.Array:
		return map[string]interface{}{
			"type":  "array",
			"items": buildFieldSchema(t.Elem()),
		}
	default:
		return scalarSchema(t)
	}
}

// scalarSchema maps a Go Kind to its JSON Schema primitive type. Any
// kind we don't recognize falls back to "string" — JSON Schema is
// forgiving and the LLM will see a clear-enough hint.
func scalarSchema(t reflect.Type) map[string]interface{} {
	switch t.Kind() {
	case reflect.String:
		return map[string]interface{}{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]interface{}{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]interface{}{"type": "number"}
	case reflect.Bool:
		return map[string]interface{}{"type": "boolean"}
	default:
		return map[string]interface{}{"type": "string"}
	}
}

// jsonFieldName parses the `json` tag the standard library way:
// "name,omitempty" or just "name" or empty (fall back to lowercase
// first character of the Go field name).
func jsonFieldName(f reflect.StructField) (name string, omitempty bool) {
	tag := f.Tag.Get("json")
	if tag == "" {
		return lowerFirst(f.Name), false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = lowerFirst(f.Name)
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func emptyObject() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

// mustJSON marshals a value that we know is JSON-safe. If marshalling
// fails it's a bug in this file (unsupported reflect kind we didn't
// guard) — panic rather than smuggling a nil json.RawMessage out.
func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("schema_gen: marshal failed: " + err.Error())
	}
	return b
}
