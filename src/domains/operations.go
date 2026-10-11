package domains

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type FieldSchema struct {
	Type                 string                 `json:"type"`
	Properties           map[string]FieldSchema `json:"properties,omitempty"`
	Required             []string               `json:"required,omitempty"`
	Items                *FieldSchema           `json:"items,omitempty"`
	Enum                 []json.RawMessage      `json:"enum,omitempty"`
	MinLength            int                    `json:"minLength,omitempty"`
	MaxLength            int                    `json:"maxLength,omitempty"`
	MinItems             int                    `json:"minItems,omitempty"`
	MaxItems             int                    `json:"maxItems,omitempty"`
	Minimum              *int64                 `json:"minimum,omitempty"`
	Maximum              *int64                 `json:"maximum,omitempty"`
	Pattern              string                 `json:"pattern,omitempty"`
	Format               string                 `json:"format,omitempty"`
	Nullable             bool                   `json:"nullable,omitempty"`
	Description          string                 `json:"description,omitempty"`
	AdditionalProperties bool                   `json:"additionalProperties"`
}
type OperationContract struct {
	Operation    string             `json:"operation"`
	Method       string             `json:"method"`
	Path         string             `json:"path"`
	Mode         string             `json:"mode"` // read, mutation or ephemeral
	Description  string             `json:"description"`
	Verification string             `json:"verification"`
	Evidence     CapabilityEvidence `json:"evidence"`
	Schedulable  bool               `json:"schedulable"`
	Request      FieldSchema        `json:"request"`
}

func ValidateJSONObject(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := decodeUniqueJSON(d, &v, 0); err != nil {
		return E("INVALID_REQUEST", "invalid or duplicate JSON fields", 400)
	}
	if _, ok := v.(map[string]any); !ok {
		return E("INVALID_REQUEST", "JSON body must be an object", 400)
	}
	if _, err := d.Token(); err != io.EOF {
		return E("INVALID_REQUEST", "JSON body must contain one object", 400)
	}
	return nil
}

func decodeUniqueJSON(d *json.Decoder, out *any, depth int) error {
	if depth > 16 {
		return fmt.Errorf("too deeply nested")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	mark, ok := t.(json.Delim)
	if !ok {
		*out = t
		return nil
	}
	switch mark {
	case '{':
		m := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid key")
			}
			if _, exists := m[s]; exists {
				return fmt.Errorf("duplicate key")
			}
			var v any
			if err := decodeUniqueJSON(d, &v, depth+1); err != nil {
				return err
			}
			m[s] = v
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid object")
		}
		*out = m
	case '[':
		a := []any{}
		for d.More() {
			if len(a) >= 4096 {
				return fmt.Errorf("too many elements")
			}
			var v any
			if err := decodeUniqueJSON(d, &v, depth+1); err != nil {
				return err
			}
			a = append(a, v)
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid array")
		}
		*out = a
	default:
		return fmt.Errorf("invalid delimiter")
	}
	return nil
}

func validateField(s FieldSchema, v any, path string, depth int) error {
	bad := func(reason string) error { return fmt.Errorf("%s %s", path, reason) }
	if depth > 16 {
		return bad("is nested too deeply")
	}
	if v == nil && s.Nullable {
		return nil
	}
	if len(s.Enum) > 0 {
		raw, _ := json.Marshal(v)
		valid := false
		for _, e := range s.Enum {
			if bytes.Equal(raw, e) {
				valid = true
				break
			}
		}
		if !valid {
			return bad("has an unsupported value")
		}
	}
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return bad("must be an object")
		}
		for _, key := range s.Required {
			if _, ok := m[key]; !ok {
				return bad("requires " + key)
			}
		}
		for key, value := range m {
			field, ok := s.Properties[key]
			if !ok {
				return bad("contains unsupported fields")
			}
			if err := validateField(field, value, path+"."+key, depth+1); err != nil {
				return err
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok || len(a) < s.MinItems || (s.MaxItems > 0 && len(a) > s.MaxItems) {
			return bad("has an invalid array length")
		}
		if s.Items == nil {
			return bad("has no item schema")
		}
		for _, value := range a {
			if err := validateField(*s.Items, value, path+"[]", depth+1); err != nil {
				return err
			}
		}
	case "string":
		x, ok := v.(string)
		if !ok || !utf8.ValidString(x) {
			return bad("must be a string")
		}
		n := utf8.RuneCountInString(x)
		if n < s.MinLength || (s.MaxLength > 0 && n > s.MaxLength) {
			return bad("has an invalid length")
		}
		if s.Pattern != "" {
			match, err := regexp.MatchString(s.Pattern, x)
			if err != nil || !match {
				return bad("has an invalid format")
			}
		}
		switch s.Format {
		case "uint32-string":
			n, e := strconv.ParseUint(x, 10, 32)
			if e != nil || n == 0 {
				return bad("must be a positive uint32 decimal string")
			}
		case "int64-string":
			n, e := strconv.ParseInt(x, 10, 64)
			if e != nil || n == 0 {
				return bad("must be a nonzero signed int64 decimal string")
			}
		case "positive-int64-string":
			n, e := strconv.ParseInt(x, 10, 64)
			if e != nil || n <= 0 {
				return bad("must be a positive int64 decimal string")
			}
		case "positive-int32-string":
			n, e := strconv.ParseInt(x, 10, 32)
			if e != nil || n <= 0 {
				return bad("must be a positive int32 decimal string")
			}
		case "phone":
			if strings.TrimSpace(x) == "" {
				return bad("must contain a phone number")
			}
		}
	case "integer":
		x, ok := v.(json.Number)
		if !ok {
			return bad("must be an integer")
		}
		n, e := x.Int64()
		if e != nil || (s.Minimum != nil && n < *s.Minimum) || (s.Maximum != nil && n > *s.Maximum) {
			return bad("is outside the allowed integer range")
		}
	case "number":
		x, ok := v.(json.Number)
		if !ok {
			return bad("must be a number")
		}
		n, e := x.Float64()
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || (s.Minimum != nil && n < float64(*s.Minimum)) || (s.Maximum != nil && n > float64(*s.Maximum)) {
			return bad("is outside the allowed numeric range")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return bad("must be a boolean")
		}
	default:
		return bad("has an unsupported schema")
	}
	return nil
}

// NormalizeOperationContract validates a provider-owned finite contract without Bale semantics.
func NormalizeOperationContract(c OperationContract, raw json.RawMessage) (json.RawMessage, Peer, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > 128<<10 || !utf8.Valid(raw) {
		return nil, Peer{}, E("INVALID_REQUEST", "operation body must be UTF-8 JSON up to 128 KiB", 400)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := decodeUniqueJSON(d, &value, 0); err != nil {
		return nil, Peer{}, E("INVALID_REQUEST", "operation body must contain one JSON object without duplicate fields", 400)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, Peer{}, E("INVALID_REQUEST", "operation body must contain one JSON object", 400)
	}
	if err := validateField(c.Request, value, "body", 0); err != nil {
		return nil, Peer{}, E("INVALID_REQUEST", err.Error(), 400)
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, Peer{}, E("INVALID_REQUEST", "operation body must be an object", 400)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, Peer{}, err
	}
	var p struct {
		Peer Peer `json:"peer"`
	}
	_ = json.Unmarshal(encoded, &p)
	return encoded, p.Peer, nil
}
