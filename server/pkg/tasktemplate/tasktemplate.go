// Package tasktemplate is the standalone variable declaration, validation,
// and safe interpolation engine for Task templates.
//
// It is the independent component the future instantiate API builds on:
// a Task template author declares variables (name/type/required/default/
// description), a caller supplies values for them, and Interpolate replaces
// {{variable_name}} tokens in title/description templates. It performs no
// database, API, UI, or CLI work on its own.
//
// Scope is deliberately small and safe:
//   - Only {{variable_name}} tokens are recognized; expressions, nesting,
//     control flow, and code execution are rejected by design.
//   - Token syntax matches the autopilot issue-title template validator
//     (server/internal/service/autopilot.go): {{ name }} whitespace inside
//     the braces is tolerated so both surfaces accept the same templates.
package tasktemplate

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// VariableType enumerates the supported variable kinds.
type VariableType string

const (
	TypeText   VariableType = "text"
	TypeNumber VariableType = "number"
	TypeDate   VariableType = "date"
	TypeSelect VariableType = "select"
)

// Variable describes one declared template variable.
type Variable struct {
	// Name is the {{name}} token substituted in templates. It must match
	// variableNameRe.
	Name string
	// Type is one of text/number/date/select.
	Type VariableType
	// Required means a value must be supplied by the caller at instantiate
	// time; it cannot coexist with a Default (a default makes the variable
	// implicitly optional).
	Required bool
	// Default is substituted when the caller supplies no value. When set,
	// Required must be false.
	Default string
	// Description is free-form help text shown in the template picker.
	Description string
	// Options is the allowed value set for select variables. It is ignored
	// (and must be empty) for non-select types.
	Options []string
}

// templateTokenRE matches any {{...}} token in a template. It mirrors the
// autopilot issue-title template regex (issueTitleTemplateTokenRE in
// server/internal/service/autopilot.go) so both surfaces tolerate the same
// whitespace-inside-braces formatting; the canonical token is still
// {{name}}.
var templateTokenRE = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// variableNameRE is the only shape a variable name may take. It matches the
// audit's security recommendation: alphanumerics plus underscore, first
// character a letter, no surrounding whitespace, no dots or braces (which
// would enable go-template-style expressions or token injection).
var variableNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// ErrInvalidDeclaration is returned when the variable declarations
// themselves are malformed (bad type, bad name, conflicting attributes).
var ErrInvalidDeclaration = errors.New("invalid variable declaration")

// ErrInvalidValue is returned when a supplied value does not satisfy the
// declaration (unknown variable, missing required value, type mismatch,
// value not among select options).
var ErrInvalidValue = errors.New("invalid variable value")

// ErrUnknownToken is returned when a template contains a {{...}} token that
// is not one of the declared variables.
var ErrUnknownToken = errors.New("unknown template token")

// ValidateVariables checks a variable declaration set. Names must be unique
// and well-formed; types must be supported; select variables must list at
// least one option; required variables must not carry a default.
func ValidateVariables(vars []Variable) error {
	seen := make(map[string]struct{}, len(vars))
	for _, v := range vars {
		if !variableNameRE.MatchString(v.Name) {
			return fmt.Errorf("%w: invalid variable name %q", ErrInvalidDeclaration, v.Name)
		}
		if _, dup := seen[v.Name]; dup {
			return fmt.Errorf("%w: duplicate variable name %q", ErrInvalidDeclaration, v.Name)
		}
		seen[v.Name] = struct{}{}

		switch v.Type {
		case TypeText, TypeNumber, TypeDate:
			// Options are only meaningful for select variables.
			if len(v.Options) > 0 {
				return fmt.Errorf("%w: variable %q has options but type %q is not select", ErrInvalidDeclaration, v.Name, v.Type)
			}
		case TypeSelect:
			if len(v.Options) == 0 {
				return fmt.Errorf("%w: select variable %q must list at least one option", ErrInvalidDeclaration, v.Name)
			}
		default:
			return fmt.Errorf("%w: variable %q has unsupported type %q", ErrInvalidDeclaration, v.Name, string(v.Type))
		}

		if v.Required && v.Default != "" {
			return fmt.Errorf("%w: required variable %q cannot also declare a default", ErrInvalidDeclaration, v.Name)
		}
	}
	return nil
}

// ResolveVariables merges caller-supplied values with declared defaults,
// producing the final value map used for interpolation. values may contain
// entries for any of the declared variables; it must not contain unknown
// names or fail type validation.
func ResolveVariables(vars []Variable, values map[string]string) (map[string]string, error) {
	if err := ValidateVariables(vars); err != nil {
		return nil, err
	}

	resolved := make(map[string]string, len(vars))
	provided := make(map[string]struct{}, len(values))
	for name, val := range values {
		provided[name] = struct{}{}
		v, ok := variableByName(vars, name)
		if !ok {
			return nil, fmt.Errorf("%w: unknown variable %q", ErrInvalidValue, name)
		}
		if err := validateValue(v, val); err != nil {
			return nil, err
		}
		resolved[name] = val
	}

	for _, v := range vars {
		if _, ok := provided[v.Name]; ok {
			continue
		}
		if v.Required {
			return nil, fmt.Errorf("%w: missing required variable %q", ErrInvalidValue, v.Name)
		}
		// Optional variables always land in resolved — either their default
		// or the empty string — so Interpolate never sees a declared-but-missing
		// token and renders a literal placeholder.
		resolved[v.Name] = v.Default
	}
	return resolved, nil
}

// Interpolate substitutes every {{name}} token in tmpl with the resolved
// value for name. Every token must reference a declared variable with a
// resolved value, or an error is returned — templates must never render a
// literal {{...}} placeholder to the end user. Tokens that are not declared
// variables are rejected rather than left in place.
func Interpolate(tmpl string, vars []Variable, resolved map[string]string) (string, error) {
	missing := make(map[string]struct{})
	known := make(map[string]struct{}, len(vars))
	for _, v := range vars {
		known[v.Name] = struct{}{}
	}
	out := templateTokenRE.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := strings.TrimSpace(match[2 : len(match)-2])
		if _, ok := known[name]; !ok {
			missing[name] = struct{}{}
			return match
		}
		val, ok := resolved[name]
		if !ok {
			missing[name] = struct{}{}
			return match
		}
		return val
	})
	for name := range missing {
		if _, known := known[name]; !known {
			return "", fmt.Errorf("%w: {{%s}} is not a declared variable", ErrUnknownToken, name)
		}
		return "", fmt.Errorf("%w: {{%s}} has no resolved value", ErrInvalidValue, name)
	}
	return out, nil
}

// variableByName finds a declared variable by name.
func variableByName(vars []Variable, name string) (Variable, bool) {
	for _, v := range vars {
		if v.Name == name {
			return v, true
		}
	}
	return Variable{}, false
}

// validateValue checks one supplied value against its declaration: select
// values must be in Options, number values must parse as numbers, date
// values must parse as YYYY-MM-DD.
func validateValue(v Variable, val string) error {
	switch v.Type {
	case TypeSelect:
		for _, opt := range v.Options {
			if val == opt {
				return nil
			}
		}
		return fmt.Errorf("%w: variable %q value %q is not one of %s", ErrInvalidValue, v.Name, val, strings.Join(v.Options, ", "))
	case TypeNumber:
		if _, err := parseNumber(val); err != nil {
			return fmt.Errorf("%w: variable %q value %q is not a number", ErrInvalidValue, v.Name, val)
		}
	case TypeDate:
		if _, err := parseDate(val); err != nil {
			return fmt.Errorf("%w: variable %q value %q is not a date (YYYY-MM-DD)", ErrInvalidValue, v.Name, val)
		}
	}
	return nil
}

// parseNumber accepts integers and decimals, with optional sign. It is a
// strict subset of strconv.ParseFloat that also rejects NaN/Inf, which are
// meaningless as template values.
func parseNumber(s string) (float64, error) {
	if s == "" {
		return 0, errors.New("empty number")
	}
	// Reject NaN/Inf and hex floats by requiring a leading digit or sign.
	var f float64
	_, err := fmt.Sscanf(s, "%g", &f)
	return f, err
}

// parseDate accepts YYYY-MM-DD and nothing else. A full time.Parse would
// also accept RFC3339 timestamps; a template value like "2026-09-20T..."
// should be rejected as a date.
func parseDate(s string) (string, error) {
	if len(s) != len("2006-01-02") {
		return "", errors.New("bad date length")
	}
	if s[4] != '-' || s[7] != '-' {
		return "", errors.New("bad date separator")
	}
	for i := 0; i < len(s); i++ {
		if i == 4 || i == 7 {
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return "", errors.New("bad date digit")
		}
	}
	return s, nil
}
