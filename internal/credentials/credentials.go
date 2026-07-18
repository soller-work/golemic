package credentials

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"golemic/internal/template"
)

// projectNameRe validates project names to prevent path traversal.
var projectNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Credentials holds the bot tokens loaded from file and/or environment.
// Token values are not exposed via String() or in error messages.
type Credentials struct {
	devToken       string
	reviewerToken  string
	devSource      string
	reviewerSource string
}

// NewFromTokens creates a Credentials from raw token strings. Used in tests.
func NewFromTokens(devToken, reviewerToken string) *Credentials {
	return &Credentials{
		devToken:       devToken,
		reviewerToken:  reviewerToken,
		devSource:      "literal",
		reviewerSource: "literal",
	}
}

// DevToken returns the dev bot token.
func (c *Credentials) DevToken() string { return c.devToken }

// ReviewerToken returns the reviewer bot token.
func (c *Credentials) ReviewerToken() string { return c.reviewerToken }

// DevSource returns the source of the dev token (file_literal, direct_env,
// template_env, or template_default). Empty string if not loaded.
func (c *Credentials) DevSource() string { return c.devSource }

// ReviewerSource returns the source of the reviewer token (file_literal,
// direct_env, template_env, or template_default). Empty string if not loaded.
func (c *Credentials) ReviewerSource() string { return c.reviewerSource }

// String returns a redacted representation — never contains token values.
func (c *Credentials) String() string {
	dev := "***set***"
	if c.devToken == "" {
		dev = "***unset***"
	}
	rev := "***set***"
	if c.reviewerToken == "" {
		rev = "***unset***"
	}
	return fmt.Sprintf("Credentials{dev=%s, reviewer=%s}", dev, rev)
}

// credentialsFile is the JSON structure of ~/.golemic/<project>/credentials.json.
type credentialsFile struct {
	DevToken      string `json:"dev_token"`
	ReviewerToken string `json:"reviewer_token"`
}

// ValidateProjectName checks whether name is a valid project name (alphanumeric,
// dots, hyphens, underscores; no path traversal). Use this to validate user input
// before passing it to Loader.Load.
func ValidateProjectName(name string) error {
	if !projectNameRe.MatchString(name) {
		return fmt.Errorf("invalid project name %q: must match %s", name, projectNameRe.String())
	}
	return nil
}

// Loader loads credentials with an injectable home directory and an optional
// template resolver. If Resolver is nil, a default EnvResolver is used.
// If LookupEnv is nil, os.LookupEnv is used.
type Loader struct {
	homeDir   string
	Resolver  template.Resolver
	LookupEnv func(string) (string, bool)
}

// NewLoader creates a Loader that resolves ~ to the given homeDir.
func NewLoader(homeDir string) *Loader {
	return &Loader{homeDir: homeDir}
}

// Load reads credentials for the given project.
// Environment variables take precedence over the file per token.
func (l *Loader) Load(project string) (*Credentials, error) {
	// Reject empty home directory (N4)
	if l.homeDir == "" {
		return nil, fmt.Errorf("empty home directory")
	}

	// Validate project name to prevent path traversal
	if !projectNameRe.MatchString(project) {
		return nil, fmt.Errorf("invalid project name %q: must match %s", project, projectNameRe.String())
	}

	credPath := filepath.Join(l.homeDir, ".golemic", project, "credentials.json")

	creds := &Credentials{}

	// Use default resolver if none was injected
	resolver := l.Resolver
	if resolver == nil {
		resolver = template.NewEnvResolver()
	}

	// TOCTOU-safe read: open, stat the descriptor, then read
	f, err := os.OpenFile(credPath, os.O_RDONLY, 0)
	var openErr error
	if err == nil {
		defer f.Close()

		// Check permissions on the opened file descriptor (follows symlinks to target)
		fi, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("failed to stat credentials file %s: %w", credPath, err)
		}
		if fi.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("credentials file %s has insecure permissions (0%o); run 'chmod 600' to restrict access",
				credPath, fi.Mode().Perm())
		}

		// Read the file contents
		fileData, err := io.ReadAll(f)
		if err != nil {
			return nil, fmt.Errorf("failed to read credentials file %s: %w", credPath, err)
		}

		// Parse with strict field checking
		var fc credentialsFile
		decoder := json.NewDecoder(bytes.NewReader(fileData))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&fc); err != nil {
			var syntaxErr *json.SyntaxError
			if errors.As(err, &syntaxErr) {
				return nil, fmt.Errorf("invalid JSON in credentials file %s at offset %d: %w",
					credPath, syntaxErr.Offset, err)
			}
			var unmarshalErr *json.UnmarshalTypeError
			if errors.As(err, &unmarshalErr) {
				return nil, fmt.Errorf("invalid type in credentials file %s at field %s: %w",
					credPath, unmarshalErr.Field, err)
			}
			return nil, fmt.Errorf("invalid JSON in credentials file %s: %w", credPath, err)
		}
		creds.devToken = fc.DevToken
		creds.reviewerToken = fc.ReviewerToken
		creds.devSource = template.SourceFileLiteral
		creds.reviewerSource = template.SourceFileLiteral
	} else if errors.Is(err, os.ErrNotExist) {
		// Save the original error so it can be wrapped in the final missing-credentials error
		// for errors.Is matching.
		openErr = err
	} else {
		return nil, fmt.Errorf("failed to open credentials file %s: %w", credPath, err)
	}

	// Environment variables override file values per token.
	// If a GOLEMIC_* env var is set, skip template resolution for that field
	// and use the env value directly.
	// Track whether each was explicitly set (not just its value) — needed for
	// determining whether the missing file is the root cause (F1).
	lookup := l.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	envDevSet := false
	envRevSet := false
	if envDev, ok := lookup("GOLEMIC_DEV_TOKEN"); ok {
		creds.devToken = envDev
		creds.devSource = "direct_env"
		envDevSet = true
	} else if creds.devSource == template.SourceFileLiteral {
		// File was read; resolve templates for dev_token
		resolved, source, err := resolver.Resolve(creds.devToken)
		if err != nil {
			return nil, fmt.Errorf("dev_token: %w", err)
		}
		creds.devToken = resolved
		creds.devSource = source
	}
	if envRev, ok := lookup("GOLEMIC_REVIEWER_TOKEN"); ok {
		creds.reviewerToken = envRev
		creds.reviewerSource = "direct_env"
		envRevSet = true
	} else if creds.reviewerSource == template.SourceFileLiteral {
		// File was read; resolve templates for reviewer_token
		resolved, source, err := resolver.Resolve(creds.reviewerToken)
		if err != nil {
			return nil, fmt.Errorf("reviewer_token: %w", err)
		}
		creds.reviewerToken = resolved
		creds.reviewerSource = source
	}

	// Validate that both tokens are present — collect all missing tokens before failing
	var missing []string
	if creds.devToken == "" {
		missing = append(missing, "dev_token (env GOLEMIC_DEV_TOKEN)")
	}
	if creds.reviewerToken == "" {
		missing = append(missing, "reviewer_token (env GOLEMIC_REVIEWER_TOKEN)")
	}
	if len(missing) > 0 {
		err := fmt.Errorf("missing credentials in %s: %v; set the corresponding environment variable or add the field to the file",
			credPath, missing)
		// Wrap openErr (os.ErrNotExist) only when no env var was set at all (F1).
		// If any env var was set, the missing file is not the root cause — the
		// absent env var or empty file field is.
		if openErr != nil && !envDevSet && !envRevSet {
			return nil, fmt.Errorf("%w: %w", err, openErr)
		}
		return nil, err
	}

	return creds, nil
}