// Package elicit implements the client side of MCP form elicitation: it checks
// a server's requestedSchema against the restricted subset the specification
// allows, flattens it into form fields, validates the user's answer against
// those fields, and carries the hook through which a front end (the TUI, a
// /v1 client) asks the user.
package elicit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// Limits on a requested schema. The specification sets none; they keep a
// hostile or broken server from sending a form no one can review.
const (
	MaxFields      = 64
	MaxOptions     = 100
	MaxNameLen     = 128
	MaxTitleLen    = 200
	MaxTextLen     = 2000
	MaxMessageLen  = 10000
	maxPatternLen  = 1000
	maxLengthBound = 1 << 20
)

// Formats are the string formats the specification allows.
var Formats = []string{"email", "uri", "date", "date-time"}

// Keywords allowed at each level. Annotations that change nothing about
// validation ($comment, $schema, $id) are accepted and ignored; any other
// keyword is outside the subset and the schema is refused.
var (
	rootKeys     = keySet("type", "properties", "required", "title", "description", "additionalProperties", "$schema", "$id", "$comment")
	commonKeys   = []string{"type", "title", "description", "default", "$comment"}
	stringKeys   = keySet(append(commonKeys, "minLength", "maxLength", "format", "pattern", "enum", "enumNames", "oneOf")...)
	numberKeys   = keySet(append(commonKeys, "minimum", "maximum")...)
	booleanKeys  = keySet(commonKeys...)
	arrayKeys    = keySet(append(commonKeys, "items", "minItems", "maxItems", "uniqueItems")...)
	itemsKeys    = keySet("type", "enum", "anyOf")
	titledKeys   = keySet("const", "title")
	choiceFields = keySet("enum", "enumNames", "oneOf")
)

func keySet(keys ...string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out
}

// Parse checks raw, an elicitation's requestedSchema, against the subset MCP
// allows and returns its properties as fields. order, when it names exactly
// the schema's properties, is the order the server wrote them in (Go maps
// lose it); otherwise required properties come first, in required order,
// then the rest by name. A missing or empty schema is a form with no fields:
// a plain confirmation.
//
// The subset: a flat object whose properties are each
//   - string: title, description, minLength, maxLength, pattern, format
//     (email, uri, date, date-time), default;
//   - string choice: enum (with optional legacy enumNames) or oneOf of
//     {const, title}, default;
//   - number or integer: title, description, minimum, maximum, default;
//   - boolean: title, description, default;
//   - multiple choice: type array, items {type: string, enum} or
//     {anyOf: [{const, title}]}, minItems, maxItems, default;
//
// and required lists property names. Anything else is refused.
func Parse(raw json.RawMessage, order []string) ([]ai.ElicitField, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return []ai.ElicitField{}, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, errors.New("requestedSchema must be a JSON object")
	}
	if err := onlyKeys(root, rootKeys, "requestedSchema"); err != nil {
		return nil, err
	}
	if t, ok := root["type"]; ok {
		var s string
		if json.Unmarshal(t, &s) != nil || s != "object" {
			return nil, errors.New(`requestedSchema type must be "object"`)
		}
	}
	if ap, ok := root["additionalProperties"]; ok {
		var b bool
		if json.Unmarshal(ap, &b) != nil {
			return nil, errors.New("requestedSchema additionalProperties must be a boolean")
		}
	}
	props := map[string]json.RawMessage{}
	if p, ok := root["properties"]; ok && !isNull(p) {
		if err := json.Unmarshal(p, &props); err != nil {
			return nil, errors.New("requestedSchema properties must be an object")
		}
	}
	if len(props) > MaxFields {
		return nil, fmt.Errorf("requestedSchema has %d properties; at most %d are supported", len(props), MaxFields)
	}
	var required []string
	if r, ok := root["required"]; ok && !isNull(r) {
		if err := json.Unmarshal(r, &required); err != nil {
			return nil, errors.New("requestedSchema required must be an array of property names")
		}
	}
	isRequired := map[string]bool{}
	for _, name := range required {
		if _, ok := props[name]; !ok {
			return nil, fmt.Errorf("required names %q, which is not a property", name)
		}
		if isRequired[name] {
			return nil, fmt.Errorf("required names %q twice", name)
		}
		isRequired[name] = true
	}
	fields := make([]ai.ElicitField, 0, len(props))
	for _, name := range fieldOrder(props, required, order) {
		f, err := parseField(name, props[name])
		if err != nil {
			return nil, err
		}
		f.Required = isRequired[name]
		fields = append(fields, f)
	}
	return fields, nil
}

func fieldOrder(props map[string]json.RawMessage, required, order []string) []string {
	if len(order) == len(props) {
		seen := map[string]bool{}
		ok := true
		for _, name := range order {
			if _, exists := props[name]; !exists || seen[name] {
				ok = false
				break
			}
			seen[name] = true
		}
		if ok {
			return order
		}
	}
	out := append([]string(nil), required...)
	var rest []string
	for name := range props {
		if !slices.Contains(required, name) {
			rest = append(rest, name)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

func parseField(name string, raw json.RawMessage) (ai.ElicitField, error) {
	f := ai.ElicitField{Name: name}
	fail := func(format string, args ...any) (ai.ElicitField, error) {
		return ai.ElicitField{}, fmt.Errorf("property %q: %s", name, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > MaxNameLen {
		return fail("property names must be 1-%d characters", MaxNameLen)
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil {
		return fail("must be a schema object")
	}
	if err := json.Unmarshal(p["type"], &f.Type); err != nil || f.Type == "" {
		return fail("type is required and must be one of string, number, integer, boolean, array")
	}
	var allowed map[string]bool
	switch f.Type {
	case "string":
		allowed = stringKeys
	case "number", "integer":
		allowed = numberKeys
	case "boolean":
		allowed = booleanKeys
	case "array":
		allowed = arrayKeys
	default:
		return fail("unsupported type %q; only string, number, integer, boolean and array (multiple choice) are allowed", f.Type)
	}
	if err := onlyKeys(p, allowed, fmt.Sprintf("property %q", name)); err != nil {
		return ai.ElicitField{}, err
	}
	var err error
	if f.Title, err = text(p, "title", MaxTitleLen); err != nil {
		return fail("%v", err)
	}
	if f.Description, err = text(p, "description", MaxTextLen); err != nil {
		return fail("%v", err)
	}
	switch f.Type {
	case "string":
		err = parseString(&f, p)
	case "number", "integer":
		err = parseNumber(&f, p)
	case "array":
		err = parseMulti(&f, p)
	}
	if err != nil {
		return fail("%v", err)
	}
	if d, ok := p["default"]; ok && !isNull(d) {
		var v any
		if err := json.Unmarshal(d, &v); err != nil {
			return fail("invalid default")
		}
		norm, msg := checkValue(f, v)
		if msg != "" {
			return fail("default %s", msg)
		}
		f.Default = norm
	}
	f.Sensitive = f.Type == "string" && len(f.Options) == 0 && looksSensitive(f.Name, f.Title)
	return f, nil
}

func parseString(f *ai.ElicitField, p map[string]json.RawMessage) error {
	choice := false
	for k := range p {
		if choiceFields[k] {
			choice = true
		}
	}
	if choice {
		for _, k := range []string{"minLength", "maxLength", "format", "pattern"} {
			if _, ok := p[k]; ok {
				return fmt.Errorf("%s cannot be combined with enum or oneOf", k)
			}
		}
		_, hasEnum := p["enum"]
		_, hasOneOf := p["oneOf"]
		switch {
		case hasEnum && hasOneOf:
			return errors.New("use either enum or oneOf, not both")
		case hasOneOf:
			if _, ok := p["enumNames"]; ok {
				return errors.New("enumNames only goes with enum")
			}
			opts, err := titledOptions(p["oneOf"], "oneOf")
			f.Options = opts
			return err
		case hasEnum:
			opts, err := enumOptions(p["enum"], p["enumNames"])
			f.Options = opts
			return err
		default:
			return errors.New("enumNames only goes with enum")
		}
	}
	var err error
	if f.MinLength, err = count(p, "minLength"); err != nil {
		return err
	}
	if f.MaxLength, err = count(p, "maxLength"); err != nil {
		return err
	}
	if f.MinLength != nil && f.MaxLength != nil && *f.MaxLength < *f.MinLength {
		return fmt.Errorf("maxLength %d is less than minLength %d", *f.MaxLength, *f.MinLength)
	}
	if raw, ok := p["format"]; ok {
		if json.Unmarshal(raw, &f.Format) != nil || !slices.Contains(Formats, f.Format) {
			return fmt.Errorf("format must be one of %s", strings.Join(Formats, ", "))
		}
	}
	if raw, ok := p["pattern"]; ok {
		if json.Unmarshal(raw, &f.Pattern) != nil || f.Pattern == "" || len(f.Pattern) > maxPatternLen {
			return fmt.Errorf("pattern must be a non-empty string of at most %d bytes", maxPatternLen)
		}
		if _, err := compilePattern(f.Pattern); err != nil {
			return fmt.Errorf("pattern is not a supported regular expression: %v", err)
		}
	}
	return nil
}

func parseNumber(f *ai.ElicitField, p map[string]json.RawMessage) error {
	var err error
	if f.Minimum, err = number(p, "minimum"); err != nil {
		return err
	}
	if f.Maximum, err = number(p, "maximum"); err != nil {
		return err
	}
	if f.Minimum != nil && f.Maximum != nil && *f.Maximum < *f.Minimum {
		return fmt.Errorf("maximum %g is less than minimum %g", *f.Maximum, *f.Minimum)
	}
	if f.Type == "integer" && f.Minimum != nil && f.Maximum != nil && math.Ceil(*f.Minimum) > math.Floor(*f.Maximum) {
		return fmt.Errorf("no integer lies between minimum %g and maximum %g", *f.Minimum, *f.Maximum)
	}
	return nil
}

func parseMulti(f *ai.ElicitField, p map[string]json.RawMessage) error {
	rawItems, ok := p["items"]
	if !ok {
		return errors.New("an array property needs items with enum or anyOf (multiple choice)")
	}
	var items map[string]json.RawMessage
	if err := json.Unmarshal(rawItems, &items); err != nil {
		return errors.New("items must be a schema object")
	}
	if err := onlyKeys(items, itemsKeys, "items"); err != nil {
		return err
	}
	_, hasEnum := items["enum"]
	_, hasAnyOf := items["anyOf"]
	var err error
	switch {
	case hasEnum && hasAnyOf:
		return errors.New("items use either enum or anyOf, not both")
	case hasEnum:
		var t string
		if json.Unmarshal(items["type"], &t) != nil || t != "string" {
			return errors.New(`items with enum must have type "string"`)
		}
		f.Options, err = enumOptions(items["enum"], nil)
	case hasAnyOf:
		if t, ok := items["type"]; ok {
			var s string
			if json.Unmarshal(t, &s) != nil || s != "string" {
				return errors.New(`items type must be "string"`)
			}
		}
		f.Options, err = titledOptions(items["anyOf"], "anyOf")
	default:
		return errors.New("items need enum or anyOf: arrays are only allowed as multiple choice")
	}
	if err != nil {
		return err
	}
	if u, ok := p["uniqueItems"]; ok {
		var b bool
		if json.Unmarshal(u, &b) != nil {
			return errors.New("uniqueItems must be a boolean")
		}
	}
	if f.MinItems, err = count(p, "minItems"); err != nil {
		return err
	}
	if f.MaxItems, err = count(p, "maxItems"); err != nil {
		return err
	}
	if f.MinItems != nil && f.MaxItems != nil && *f.MaxItems < *f.MinItems {
		return fmt.Errorf("maxItems %d is less than minItems %d", *f.MaxItems, *f.MinItems)
	}
	if f.MinItems != nil && *f.MinItems > len(f.Options) {
		return fmt.Errorf("minItems %d exceeds the %d options", *f.MinItems, len(f.Options))
	}
	return nil
}

func enumOptions(rawEnum, rawNames json.RawMessage) ([]ai.ElicitOption, error) {
	var values []string
	if err := json.Unmarshal(rawEnum, &values); err != nil {
		return nil, errors.New("enum must be an array of strings")
	}
	var names []string
	if rawNames != nil && !isNull(rawNames) {
		if err := json.Unmarshal(rawNames, &names); err != nil {
			return nil, errors.New("enumNames must be an array of strings")
		}
		if len(names) != len(values) {
			return nil, fmt.Errorf("enum has %d values but enumNames has %d", len(values), len(names))
		}
	}
	opts := make([]ai.ElicitOption, len(values))
	for i, v := range values {
		label := v
		if names != nil {
			label = names[i]
		}
		opts[i] = ai.ElicitOption{Value: v, Label: label}
	}
	return opts, checkOptions(opts)
}

func titledOptions(raw json.RawMessage, key string) ([]ai.ElicitOption, error) {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%s must be an array of {const, title}", key)
	}
	opts := make([]ai.ElicitOption, len(entries))
	for i, e := range entries {
		if err := onlyKeys(e, titledKeys, key+" entry"); err != nil {
			return nil, err
		}
		if json.Unmarshal(e["const"], &opts[i].Value) != nil {
			return nil, fmt.Errorf("%s entries need a string const", key)
		}
		if json.Unmarshal(e["title"], &opts[i].Label) != nil || strings.TrimSpace(opts[i].Label) == "" {
			return nil, fmt.Errorf("%s entries need a non-empty title", key)
		}
	}
	return opts, checkOptions(opts)
}

func checkOptions(opts []ai.ElicitOption) error {
	if len(opts) == 0 || len(opts) > MaxOptions {
		return fmt.Errorf("a choice needs 1-%d options", MaxOptions)
	}
	seen := map[string]bool{}
	for _, o := range opts {
		if o.Value == "" {
			return errors.New("option values cannot be empty")
		}
		if seen[o.Value] {
			return fmt.Errorf("option %q appears twice", o.Value)
		}
		if utf8.RuneCountInString(o.Value) > MaxTitleLen*2 || utf8.RuneCountInString(o.Label) > MaxTitleLen*2 {
			return fmt.Errorf("option %q is too long", ellipsis(o.Value))
		}
		seen[o.Value] = true
	}
	return nil
}

func onlyKeys(m map[string]json.RawMessage, allowed map[string]bool, where string) error {
	var bad []string
	for k := range m {
		if !allowed[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	slices.Sort(bad)
	return fmt.Errorf("%s: keyword %q is outside the elicitation schema subset", where, bad[0])
}

func text(p map[string]json.RawMessage, key string, limit int) (string, error) {
	raw, ok := p[key]
	if !ok || isNull(raw) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if utf8.RuneCountInString(s) > limit {
		return "", fmt.Errorf("%s is longer than %d characters", key, limit)
	}
	return strings.TrimSpace(s), nil
}

func count(p map[string]json.RawMessage, key string) (*int, error) {
	raw, ok := p[key]
	if !ok || isNull(raw) {
		return nil, nil
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || f < 0 || f != math.Trunc(f) || f > maxLengthBound {
		return nil, fmt.Errorf("%s must be a non-negative integer", key)
	}
	n := int(f)
	return &n, nil
}

func number(p map[string]json.RawMessage, key string) (*float64, error) {
	raw, ok := p[key]
	if !ok || isNull(raw) {
		return nil, nil
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, fmt.Errorf("%s must be a number", key)
	}
	return &f, nil
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func ellipsis(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

var patternCache = struct {
	m map[string]*regexp.Regexp
}{m: map[string]*regexp.Regexp{}}

func compilePattern(p string) (*regexp.Regexp, error) {
	patternMu.Lock()
	defer patternMu.Unlock()
	if re, ok := patternCache.m[p]; ok {
		return re, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	if len(patternCache.m) > 256 {
		clear(patternCache.m)
	}
	patternCache.m[p] = re
	return re, nil
}
