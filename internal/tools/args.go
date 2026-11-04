package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// decodeArgs decodes a core tool's arguments into dst, a pointer to a struct with
// json tags. Models often quote scalars ("true", "120") or send the whole object as
// a JSON string; both are accepted. Anything else that does not fit is reported by
// argument name and expected type rather than as a Go decoding error.
func decodeArgs(tool string, raw json.RawMessage, dst any, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		var inner string
		if json.Unmarshal(raw, &inner) != nil || json.Unmarshal([]byte(inner), &fields) != nil {
			return fmt.Errorf("%s: arguments must be a JSON object of named arguments (%s)", tool, describeJSON(raw))
		}
	}
	if fields == nil {
		return fmt.Errorf("%s: arguments must be a JSON object of named arguments, got null", tool)
	}
	present := func(name string) bool {
		v, ok := fields[name]
		return ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null"))
	}
	for _, name := range required {
		if !present(name) {
			return fmt.Errorf("%s: missing required argument %q", tool, name)
		}
	}
	v := reflect.ValueOf(dst).Elem()
	for i := range v.NumField() {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" || !present(name) {
			continue
		}
		field, value := v.Field(i), fields[name]
		if json.Unmarshal(value, field.Addr().Interface()) == nil || coerceArg(field, value) {
			continue
		}
		return fmt.Errorf("%s: argument %q must be %s, got %s", tool, name, expectedKind(field.Kind()), describeJSON(value))
	}
	return nil
}

// coerceArg sets field from a JSON value of the wrong but unambiguous type: a quoted
// boolean or number, a whole float for an integer, or a scalar for a string.
func coerceArg(field reflect.Value, value json.RawMessage) bool {
	var text string
	if json.Unmarshal(value, &text) != nil {
		text = string(bytes.TrimSpace(value))
	}
	text = strings.TrimSpace(text)
	switch field.Kind() {
	case reflect.Bool:
		switch strings.ToLower(text) {
		case "true", "1":
			field.SetBool(true)
			return true
		case "false", "0":
			field.SetBool(false)
			return true
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseFloat(text, 64)
		if err != nil || n != math.Trunc(n) || math.IsInf(n, 0) || field.OverflowInt(int64(n)) {
			return false
		}
		field.SetInt(int64(n))
		return true
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return false
		}
		field.SetFloat(n)
		return true
	case reflect.String:
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			return false
		}
		field.SetString(string(trimmed))
		return true
	}
	return false
}

func expectedKind(kind reflect.Kind) string {
	switch kind {
	case reflect.Bool:
		return "a boolean (true or false)"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.String:
		return "a string"
	}
	return "a " + kind.String()
}

// describeJSON names a JSON value's type and shows a short excerpt of it.
func describeJSON(value json.RawMessage) string {
	trimmed := bytes.TrimSpace(value)
	excerpt := clipText(string(trimmed), 60)
	if len(trimmed) == 0 {
		return "nothing"
	}
	if !json.Valid(trimmed) {
		return "invalid JSON " + excerpt
	}
	switch trimmed[0] {
	case '"':
		return "string " + excerpt
	case '{':
		return "an object"
	case '[':
		return "an array"
	case 't', 'f':
		return "boolean " + excerpt
	case 'n':
		return "null"
	}
	return "number " + excerpt
}
