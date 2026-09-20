package tasktemplate

import (
	"errors"
	"strings"
	"testing"
)

// declaredVars is a representative variable set exercising every supported
// type plus required/optional/default combinations.
func declaredVars() []Variable {
	return []Variable{
		{Name: "summary", Type: TypeText, Required: true, Description: "one-line summary"},
		{Name: "component", Type: TypeSelect, Required: true, Options: []string{"api", "web", "cli"}},
		{Name: "severity", Type: TypeSelect, Options: []string{"low", "high"}},
		{Name: "estimate", Type: TypeNumber},
		{Name: "due", Type: TypeDate},
		{Name: "assignee", Type: TypeText, Default: "triage"},
	}
}

func TestValidateVariables(t *testing.T) {
	t.Parallel()

	t.Run("accepts a valid declaration set", func(t *testing.T) {
		if err := ValidateVariables(declaredVars()); err != nil {
			t.Fatalf("ValidateVariables = %v, want nil", err)
		}
	})

	t.Run("accepts empty declaration set", func(t *testing.T) {
		if err := ValidateVariables(nil); err != nil {
			t.Fatalf("ValidateVariables(nil) = %v, want nil", err)
		}
	})

	bad := []struct {
		name string
		vars []Variable
		want string
	}{
		{"invalid variable name with dot", []Variable{{Name: "a.b", Type: TypeText}}, "invalid variable name"},
		{"invalid variable name starting with digit", []Variable{{Name: "1x", Type: TypeText}}, "invalid variable name"},
		{"invalid variable name with dash", []Variable{{Name: "a-b", Type: TypeText}}, "invalid variable name"},
		{"duplicate variable name", []Variable{{Name: "x", Type: TypeText}, {Name: "x", Type: TypeText}}, "duplicate variable name"},
		{"unsupported type", []Variable{{Name: "x", Type: VariableType("bool")}}, "unsupported type"},
		{"select without options", []Variable{{Name: "x", Type: TypeSelect}}, "must list at least one option"},
		{"non-select with options", []Variable{{Name: "x", Type: TypeText, Options: []string{"a"}}}, "not select"},
		{"required with default", []Variable{{Name: "x", Type: TypeText, Required: true, Default: "v"}}, "cannot also declare a default"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateVariables(tc.vars)
			if err == nil {
				t.Fatalf("ValidateVariables = nil, want error containing %q", tc.want)
			}
			if !errors.Is(err, ErrInvalidDeclaration) {
				t.Fatalf("error should wrap ErrInvalidDeclaration: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestResolveVariables(t *testing.T) {
	t.Parallel()

	t.Run("fills defaults and keeps supplied values", func(t *testing.T) {
		got, err := ResolveVariables(declaredVars(), map[string]string{
			"summary":   "login fails",
			"component": "api",
		})
		if err != nil {
			t.Fatalf("ResolveVariables = %v, want nil", err)
		}
		want := map[string]string{
			"summary":   "login fails",
			"component": "api",
			"severity":  "",
			"estimate":  "",
			"due":       "",
			"assignee":  "triage",
		}
		if len(got) != len(want) {
			t.Fatalf("resolved %d values, want %d: %v", len(got), len(want), got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("resolved[%q] = %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("rejects missing required variable", func(t *testing.T) {
		_, err := ResolveVariables(declaredVars(), map[string]string{"component": "api"})
		if err == nil || !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("err = %v, want ErrInvalidValue", err)
		}
		if !strings.Contains(err.Error(), `missing required variable "summary"`) {
			t.Fatalf("error should name missing variable: %v", err)
		}
	})

	t.Run("rejects unknown variable", func(t *testing.T) {
		_, err := ResolveVariables(declaredVars(), map[string]string{"summary": "x", "nope": "y"})
		if err == nil || !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("err = %v, want ErrInvalidValue", err)
		}
		if !strings.Contains(err.Error(), `unknown variable "nope"`) {
			t.Fatalf("error should name unknown variable: %v", err)
		}
	})

	t.Run("rejects select value outside options", func(t *testing.T) {
		_, err := ResolveVariables(declaredVars(), map[string]string{"summary": "x", "component": "bogus"})
		if err == nil || !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("err = %v, want ErrInvalidValue", err)
		}
	})

	t.Run("rejects non-number for number variable", func(t *testing.T) {
		_, err := ResolveVariables(declaredVars(), map[string]string{"summary": "x", "component": "api", "estimate": "abc"})
		if err == nil || !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("err = %v, want ErrInvalidValue", err)
		}
	})

	t.Run("rejects malformed date", func(t *testing.T) {
		_, err := ResolveVariables(declaredVars(), map[string]string{"summary": "x", "component": "api", "due": "09/20/2026"})
		if err == nil || !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("err = %v, want ErrInvalidValue", err)
		}
	})
}

func TestInterpolate(t *testing.T) {
	t.Parallel()

	vars := declaredVars()
	resolved, err := ResolveVariables(vars, map[string]string{
		"summary":   "login fails",
		"component": "api",
	})
	if err != nil {
		t.Fatalf("ResolveVariables = %v", err)
	}

	t.Run("substitutes every declared token", func(t *testing.T) {
		in := "[Bug] {{summary}} — {{component}} / est {{estimate}} / due {{due}} / {{assignee}}"
		got, err := Interpolate(in, vars, resolved)
		if err != nil {
			t.Fatalf("Interpolate = %v, want nil", err)
		}
		want := "[Bug] login fails — api / est  / due  / triage"
		if got != want {
			t.Fatalf("Interpolate = %q, want %q", got, want)
		}
	})

	t.Run("tolerates whitespace inside braces", func(t *testing.T) {
		got, err := Interpolate("{{ summary }}", vars, resolved)
		if err != nil {
			t.Fatalf("Interpolate = %v, want nil", err)
		}
		if got != "login fails" {
			t.Fatalf("Interpolate = %q, want %q", got, "login fails")
		}
	})

	t.Run("leaves text without tokens untouched", func(t *testing.T) {
		got, err := Interpolate("plain title", vars, resolved)
		if err != nil {
			t.Fatalf("Interpolate = %v, want nil", err)
		}
		if got != "plain title" {
			t.Fatalf("Interpolate = %q, want %q", got, "plain title")
		}
	})

	t.Run("empty template is valid", func(t *testing.T) {
		got, err := Interpolate("", vars, resolved)
		if err != nil {
			t.Fatalf("Interpolate = %v, want nil", err)
		}
		if got != "" {
			t.Fatalf("Interpolate = %q, want empty", got)
		}
	})

	t.Run("rejects unknown token", func(t *testing.T) {
		_, err := Interpolate("{{summary}} {{trigger_id}}", vars, resolved)
		if err == nil || !errors.Is(err, ErrUnknownToken) {
			t.Fatalf("err = %v, want ErrUnknownToken", err)
		}
		if !strings.Contains(err.Error(), "trigger_id") {
			t.Fatalf("error should name the unknown token: %v", err)
		}
	})

	t.Run("optional variable without value or default interpolates empty", func(t *testing.T) {
		// estimate/due are declared, optional, and carry no default and no
		// supplied value: ResolveVariables fills them with "", so rendering
		// must produce empty text rather than a literal placeholder.
		got, err := Interpolate("[Bug] {{summary}} ({{estimate}})", vars, resolved)
		if err != nil {
			t.Fatalf("Interpolate = %v, want nil", err)
		}
		if want := "[Bug] login fails ()"; got != want {
			t.Fatalf("Interpolate = %q, want %q", got, want)
		}
	})

	t.Run("rejects go-template style token", func(t *testing.T) {
		_, err := Interpolate("{{.TriggeredAt}}", vars, resolved)
		if err == nil || !errors.Is(err, ErrUnknownToken) {
			t.Fatalf("err = %v, want ErrUnknownToken", err)
		}
	})
}

// TestInterpolateReusesAutopilotTokenSyntax locks in that the engine shares
// the autopilot issue-title token shape: {{ name }} with optional inner
// whitespace. If the autopilot surface's validator ever changes shape, this
// regression test points the fix here too.
func TestInterpolateReusesAutopilotTokenSyntax(t *testing.T) {
	t.Parallel()

	vars := []Variable{{Name: "date", Type: TypeText, Default: "2026-09-20"}}
	resolved, err := ResolveVariables(vars, nil)
	if err != nil {
		t.Fatalf("ResolveVariables = %v", err)
	}

	for _, in := range []string{"report {{date}}", "report {{ date }}"} {
		got, err := Interpolate(in, vars, resolved)
		if err != nil {
			t.Fatalf("Interpolate(%q) = %v, want nil", in, err)
		}
		if want := "report 2026-09-20"; got != want {
			t.Fatalf("Interpolate(%q) = %q, want %q", in, got, want)
		}
	}
}
