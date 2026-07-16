// Package template resolves ${ENV_VAR} and ${ENV_VAR:default} template references
// in credential values. It provides the Resolver interface so the credentials
// package can inject a resolver without creating a circular dependency.
package template

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Source values returned by Resolve.
const (
	SourceFileLiteral     = "file_literal"
	SourceTemplateEnv     = "template_env"
	SourceTemplateDefault = "template_default"
)

// Resolver resolves template references in a string value.
type Resolver interface {
	// Resolve processes value and replaces any ${VAR} or ${VAR:default}
	// patterns using the current process environment. It returns the resolved
	// string, a source tag (file_literal, template_env, or template_default),
	// and any error encountered.
	Resolve(value string) (string, string, error)
}

// EnvResolver implements Resolver using os.LookupEnv.
type EnvResolver struct{}

// NewEnvResolver creates a Resolver that resolves templates against the
// current process environment.
func NewEnvResolver() *EnvResolver {
	return &EnvResolver{}
}

// varNameRe matches valid environment variable names: [A-Za-z_][A-Za-z0-9_]*
var varNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MalformedTemplateError indicates a syntax error in a template reference.
// The Description field describes what is wrong without including token values
// or environment variable values.
type MalformedTemplateError struct {
	Description string
}

func (e *MalformedTemplateError) Error() string {
	return fmt.Sprintf("malformed template reference: %s", e.Description)
}

// MissingEnvVarError indicates a referenced environment variable is not set
// and no default value was provided.
type MissingEnvVarError struct {
	VarName string
}

func (e *MissingEnvVarError) Error() string {
	return fmt.Sprintf("environment variable %s is not set", e.VarName)
}

// Resolve processes value and replaces any ${VAR} or ${VAR:default} patterns
// using os.LookupEnv.
func (r *EnvResolver) Resolve(value string) (string, string, error) {
	// Fast path: no template syntax
	if !strings.Contains(value, "${") {
		return value, SourceFileLiteral, nil
	}

	var buf strings.Builder
	source := SourceFileLiteral
	remainder := value

	for {
		idx := strings.Index(remainder, "${")
		if idx < 0 {
			buf.WriteString(remainder)
			break
		}

		// Write the literal prefix before this template
		buf.WriteString(remainder[:idx])
		remainder = remainder[idx:]

		// Find matching closing brace
		end := strings.Index(remainder, "}")
		if end < 0 {
			return "", "", &MalformedTemplateError{Description: "unclosed template reference"}
		}

		// Extract content between ${ and }
		content := remainder[2:end]
		if content == "" {
			return "", "", &MalformedTemplateError{Description: "empty template reference"}
		}

		// Parse variable name and optional default value
		var varName, defaultVal string
		hasDefault := false
		if colonIdx := strings.Index(content, ":"); colonIdx >= 0 {
			varName = content[:colonIdx]
			defaultVal = content[colonIdx+1:]
			hasDefault = true
		} else {
			varName = content
		}

		// Validate variable name
		if !varNameRe.MatchString(varName) {
			return "", "", &MalformedTemplateError{
				Description: fmt.Sprintf("invalid variable name in template: %s", varName),
			}
		}

		// Look up environment variable
		envVal, ok := os.LookupEnv(varName)
		if ok && envVal != "" {
			buf.WriteString(envVal)
			if source == SourceFileLiteral {
				source = SourceTemplateEnv
			}
		} else if hasDefault {
			buf.WriteString(defaultVal)
			if source == SourceFileLiteral {
				source = SourceTemplateDefault
			}
		} else {
			return "", "", &MissingEnvVarError{VarName: varName}
		}

		remainder = remainder[end+1:]
	}

	return buf.String(), source, nil
}