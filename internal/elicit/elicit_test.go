package elicit

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func mustParse(t *testing.T, schema string, order ...string) []ai.ElicitField {
	t.Helper()
	fields, err := Parse(json.RawMessage(schema), order)
	if err != nil {
		t.Fatalf("Parse(%s): %v", schema, err)
	}
	return fields
}

func TestParseFullSubset(t *testing.T) {
	schema := `{
		"type": "object",
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"properties": {
			"name": {"type": "string", "title": "Name", "description": "Your name", "minLength": 1, "maxLength": 50, "default": "Ada"},
			"email": {"type": "string", "format": "email"},
			"code": {"type": "string", "pattern": "^[A-Z]{3}$"},
			"age": {"type": "integer", "minimum": 0, "maximum": 150},
			"ratio": {"type": "number", "minimum": 0.5, "default": 1},
			"ok": {"type": "boolean", "default": true},
			"color": {"type": "string", "enum": ["r", "g"], "enumNames": ["Red", "Green"], "default": "g"},
			"size": {"type": "string", "oneOf": [{"const": "s", "title": "Small"}, {"const": "l", "title": "Large"}]},
			"tags": {"type": "array", "items": {"type": "string", "enum": ["a", "b", "c"]}, "minItems": 1, "maxItems": 2, "default": ["b", "a"]},
			"pets": {"type": "array", "items": {"anyOf": [{"const": "cat", "title": "Cat"}, {"const": "dog", "title": "Dog"}]}}
		},
		"required": ["name", "age"]
	}`
	fields := mustParse(t, schema)
	var names []string
	for _, f := range fields {
		names = append(names, f.Name)
	}
	// Without a server order: required in required order, then by name.
	want := []string{"name", "age", "code", "color", "email", "ok", "pets", "ratio", "size", "tags"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
	by := map[string]ai.ElicitField{}
	for _, f := range fields {
		by[f.Name] = f
	}
	if f := by["name"]; !f.Required || f.Title != "Name" || *f.MinLength != 1 || *f.MaxLength != 50 || f.Default != "Ada" {
		t.Errorf("name = %+v", f)
	}
	if f := by["color"]; !reflect.DeepEqual(f.Options, []ai.ElicitOption{{Value: "r", Label: "Red"}, {Value: "g", Label: "Green"}}) || f.Default != "g" {
		t.Errorf("color = %+v", f)
	}
	if f := by["size"]; !reflect.DeepEqual(f.Options, []ai.ElicitOption{{Value: "s", Label: "Small"}, {Value: "l", Label: "Large"}}) {
		t.Errorf("size = %+v", f)
	}
	if f := by["tags"]; f.Type != "array" || len(f.Options) != 3 || f.Options[0].Label != "a" || !reflect.DeepEqual(f.Default, []any{"a", "b"}) {
		t.Errorf("tags = %+v (default is normalized to option order)", f)
	}
	if f := by["pets"]; f.Options[1] != (ai.ElicitOption{Value: "dog", Label: "Dog"}) {
		t.Errorf("pets = %+v", f)
	}
	if f := by["ratio"]; f.Default != float64(1) || *f.Minimum != 0.5 {
		t.Errorf("ratio = %+v", f)
	}
	if f := by["ok"]; f.Default != true {
		t.Errorf("ok = %+v", f)
	}
}

func TestParseServerOrder(t *testing.T) {
	schema := `{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"string"}},"required":["a"]}`
	fields := mustParse(t, schema, "b", "a")
	if fields[0].Name != "b" || fields[1].Name != "a" || !fields[1].Required {
		t.Fatalf("fields = %+v", fields)
	}
	// An order that does not name exactly the properties is ignored.
	fields = mustParse(t, schema, "b", "zzz")
	if fields[0].Name != "a" {
		t.Fatalf("fallback order = %+v", fields)
	}
}

func TestParseEmpty(t *testing.T) {
	for _, s := range []string{``, `null`, `{}`, `{"type":"object"}`, `{"type":"object","properties":{}}`} {
		fields, err := Parse(json.RawMessage(s), nil)
		if err != nil || fields == nil || len(fields) != 0 {
			t.Errorf("Parse(%q) = %v, %v; want an empty form", s, fields, err)
		}
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		`[]`:                                `must be a JSON object`,
		`{"type":"array"}`:                  `type must be "object"`,
		`{"type":"object","allOf":[]}`:      `keyword "allOf" is outside`,
		`{"type":"object","properties":[]}`: `properties must be an object`,
		`{"type":"object","properties":{"a":{"type":"string"}},"required":["b"]}`:     `not a property`,
		`{"type":"object","properties":{"a":{"type":"string"}},"required":["a","a"]}`: `twice`,
		`{"properties":{"a":{}}}`:                                                                      `type is required`,
		`{"properties":{"a":{"type":"object","properties":{}}}}`:                                       `unsupported type "object"`,
		`{"properties":{"a":{"type":"null"}}}`:                                                         `unsupported type`,
		`{"properties":{"a":{"type":["string","null"]}}}`:                                              `type is required`,
		`{"properties":{"a":{"type":"string","$ref":"#/x"}}}`:                                          `keyword "$ref"`,
		`{"properties":{"a":{"type":"string","properties":{}}}}`:                                       `keyword "properties"`,
		`{"properties":{"a":{"type":"number","minLength":1}}}`:                                         `keyword "minLength"`,
		`{"properties":{"a":{"type":"string","format":"ipv4"}}}`:                                       `format must be one of`,
		`{"properties":{"a":{"type":"string","pattern":"(?<=x)"}}}`:                                    `pattern is not a supported`,
		`{"properties":{"a":{"type":"string","minLength":5,"maxLength":2}}}`:                           `less than minLength`,
		`{"properties":{"a":{"type":"string","minLength":-1}}}`:                                        `non-negative integer`,
		`{"properties":{"a":{"type":"integer","minimum":1.2,"maximum":1.8}}}`:                          `no integer`,
		`{"properties":{"a":{"type":"number","minimum":"1"}}}`:                                         `minimum must be a number`,
		`{"properties":{"a":{"type":"string","enum":[]}}}`:                                             `1-100 options`,
		`{"properties":{"a":{"type":"string","enum":["x","x"]}}}`:                                      `appears twice`,
		`{"properties":{"a":{"type":"string","enum":[1,2]}}}`:                                          `enum must be an array of strings`,
		`{"properties":{"a":{"type":"string","enum":["x"],"enumNames":["X","Y"]}}}`:                    `enumNames has 2`,
		`{"properties":{"a":{"type":"string","enumNames":["X"]}}}`:                                     `enumNames only goes with enum`,
		`{"properties":{"a":{"type":"string","enum":["x"],"format":"email"}}}`:                         `cannot be combined`,
		`{"properties":{"a":{"type":"string","oneOf":[{"const":"x"}]}}}`:                               `non-empty title`,
		`{"properties":{"a":{"type":"string","oneOf":[{"const":"x","title":"X","description":"d"}]}}}`: `keyword "description"`,
		`{"properties":{"a":{"type":"array"}}}`:                                                        `needs items`,
		`{"properties":{"a":{"type":"array","items":{"type":"string"}}}}`:                              `only allowed as multiple choice`,
		`{"properties":{"a":{"type":"array","items":{"type":"number","enum":["1"]}}}}`:                 `must have type "string"`,
		`{"properties":{"a":{"type":"array","items":{"type":"string","enum":["x"]},"minItems":2}}}`:    `exceeds the 1 options`,
		`{"properties":{"a":{"type":"string","default":5}}}`:                                           `default must be a string`,
		`{"properties":{"a":{"type":"string","enum":["x"],"default":"y"}}}`:                            `default must be one of`,
		`{"properties":{"a":{"type":"integer","default":1.5}}}`:                                        `default must be a whole number`,
		`{"properties":{"a":{"type":"string","title":5}}}`:                                             `title must be a string`,
	}
	for schema, want := range cases {
		_, err := Parse(json.RawMessage(schema), nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%s) = %v, want error containing %q", schema, err, want)
		}
	}
}

func TestParseLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"type":"object","properties":{`)
	for i := 0; i <= MaxFields; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"f` + strings.Repeat("x", i) + `":{"type":"boolean"}`)
	}
	b.WriteString(`}}`)
	if _, err := Parse(json.RawMessage(b.String()), nil); err == nil || !strings.Contains(err.Error(), "at most 64") {
		t.Fatalf("too many fields: %v", err)
	}
}

func TestValidate(t *testing.T) {
	fields := mustParse(t, `{"type":"object","properties":{
		"name": {"type": "string", "minLength": 2, "maxLength": 4},
		"email": {"type": "string", "format": "email"},
		"site": {"type": "string", "format": "uri"},
		"day": {"type": "string", "format": "date"},
		"at": {"type": "string", "format": "date-time"},
		"code": {"type": "string", "pattern": "^[A-Z]{3}$"},
		"n": {"type": "integer", "minimum": 1, "maximum": 10},
		"x": {"type": "number", "maximum": 2.5},
		"ok": {"type": "boolean"},
		"color": {"type": "string", "enum": ["r", "g"]},
		"tags": {"type": "array", "items": {"type": "string", "enum": ["a", "b", "c"]}, "minItems": 1, "maxItems": 2}
	},"required":["name"]}`)

	good := map[string]any{
		"name": "日本語", "email": "a@b.co", "site": "https://x.test/p", "day": "2026-01-02",
		"at": "2026-01-02T03:04:05+08:00", "code": "ABC", "n": float64(3), "x": 2.5, "ok": false,
		"color": "g", "tags": []any{"c", "a"}, "email2": nil,
	}
	delete(good, "email2")
	out, errs := Validate(fields, good)
	if errs != nil {
		t.Fatalf("Validate(good) = %v", errs)
	}
	if !reflect.DeepEqual(out["tags"], []any{"a", "c"}) {
		t.Errorf("tags normalized to %v, want option order", out["tags"])
	}
	// Optional nulls are dropped; json.Number and ints are accepted.
	out, errs = Validate(fields, map[string]any{"name": "ab", "email": nil, "n": json.Number("4"), "x": 1})
	if errs != nil || out["n"] != float64(4) || out["x"] != float64(1) {
		t.Fatalf("Validate(numbers) = %v, %v", out, errs)
	}
	if _, ok := out["email"]; ok {
		t.Errorf("null optional field kept: %v", out)
	}

	bad := []struct {
		content map[string]any
		field   string
		want    string
	}{
		{map[string]any{}, "name", "is required"},
		{map[string]any{"name": nil}, "name", "is required"},
		{map[string]any{"name": "a"}, "name", "at least 2 characters"},
		{map[string]any{"name": "abcde"}, "name", "at most 4 characters"},
		{map[string]any{"name": 12}, "name", "must be a string"},
		{map[string]any{"name": "ab", "email": "Ada <a@b.co>"}, "email", "email address"},
		{map[string]any{"name": "ab", "email": "nope"}, "email", "email address"},
		{map[string]any{"name": "ab", "site": "/relative"}, "site", "absolute URI"},
		{map[string]any{"name": "ab", "day": "2026-13-01"}, "day", "YYYY-MM-DD"},
		{map[string]any{"name": "ab", "at": "2026-01-02 03:04"}, "at", "RFC 3339"},
		{map[string]any{"name": "ab", "code": "abc"}, "code", "pattern"},
		{map[string]any{"name": "ab", "n": 2.5}, "n", "whole number"},
		{map[string]any{"name": "ab", "n": float64(0)}, "n", "at least 1"},
		{map[string]any{"name": "ab", "x": float64(3)}, "x", "at most 2.5"},
		{map[string]any{"name": "ab", "n": "3"}, "n", "must be a number"},
		{map[string]any{"name": "ab", "ok": "true"}, "ok", "true or false"},
		{map[string]any{"name": "ab", "color": "b"}, "color", "listed choices"},
		{map[string]any{"name": "ab", "tags": []any{}}, "tags", "at least 1"},
		{map[string]any{"name": "ab", "tags": []any{"a", "b", "c"}}, "tags", "at most 2"},
		{map[string]any{"name": "ab", "tags": []any{"a", "a"}}, "tags", "chosen twice"},
		{map[string]any{"name": "ab", "tags": []any{"z"}}, "tags", "not one of"},
		{map[string]any{"name": "ab", "tags": "a"}, "tags", "list of choices"},
		{map[string]any{"name": "ab", "extra": 1}, "", `"extra" is not a field`},
	}
	for _, c := range bad {
		_, errs := Validate(fields, c.content)
		if !strings.Contains(errs[c.field], c.want) {
			t.Errorf("Validate(%v)[%q] = %q (all: %v), want %q", c.content, c.field, errs[c.field], errs, c.want)
		}
	}
	if got := CheckField(fields[0], "a"); !strings.Contains(got, "at least 2") {
		t.Errorf("CheckField = %q", got)
	}
	if got := CheckField(fields[0], nil); got != "is required" {
		t.Errorf("CheckField(nil) = %q", got)
	}
}

func TestErrorsString(t *testing.T) {
	e := Errors{"b": "is required", "": "x is not a field", "a": "must be a number"}
	if got := e.Error(); got != "x is not a field; a: must be a number; b: is required" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestSensitive(t *testing.T) {
	yes := [][2]string{
		{"password", ""}, {"apiKey", ""}, {"api_key", ""}, {"x", "API key"}, {"githubToken", ""},
		{"otp", ""}, {"pin", ""}, {"card_number", ""}, {"x", "Your secret phrase"}, {"privateKey", ""},
		{"client_secret", ""}, {"x", "Verification code"}, {"cvv", ""}, {"AWSAccessKey", ""},
	}
	no := [][2]string{
		{"name", "Full name"}, {"email", ""}, {"tokenizer", ""}, {"spinner", "Spin count"}, {"shipping", ""},
		{"topic", ""}, {"keyboard", ""}, {"x", "Pinned version"}, {"description", ""},
	}
	for _, c := range yes {
		if !looksSensitive(c[0], c[1]) {
			t.Errorf("looksSensitive(%q, %q) = false", c[0], c[1])
		}
	}
	for _, c := range no {
		if looksSensitive(c[0], c[1]) {
			t.Errorf("looksSensitive(%q, %q) = true", c[0], c[1])
		}
	}
	fields := mustParse(t, `{"properties":{
		"user":{"type":"string"},
		"password":{"type":"string"},
		"token":{"type":"string","enum":["a"]}
	}}`)
	by := map[string]bool{}
	for _, f := range fields {
		by[f.Name] = f.Sensitive
	}
	if by["user"] || !by["password"] || by["token"] {
		t.Fatalf("Sensitive = %v (choices are never sensitive)", by)
	}
	content := map[string]any{"user": "ada", "password": "hunter2"}
	kept, redacted := Redact(fields, content)
	if !reflect.DeepEqual(kept, map[string]any{"user": "ada"}) || !reflect.DeepEqual(redacted, []string{"password"}) {
		t.Fatalf("Redact = %v, %v", kept, redacted)
	}
	if content["password"] != "hunter2" {
		t.Fatal("Redact changed its input")
	}
	if kept, redacted := Redact(fields, nil); kept != nil || redacted != nil {
		t.Fatalf("Redact(nil) = %v, %v", kept, redacted)
	}
}

func TestHandler(t *testing.T) {
	old := Default
	t.Cleanup(func() { Default = old })
	Default = nil
	if Handler(context.Background()) != nil {
		t.Fatal("Handler without front end should be nil")
	}
	Default = func(context.Context, Request) Response { return Response{Action: Decline} }
	if Handler(context.Background())(context.Background(), Request{}).Action != Decline {
		t.Fatal("Default not used")
	}
	ctx := With(context.Background(), func(context.Context, Request) Response { return Response{Action: Accept} })
	if Handler(ctx)(ctx, Request{}).Action != Accept {
		t.Fatal("context hook not preferred")
	}
}
