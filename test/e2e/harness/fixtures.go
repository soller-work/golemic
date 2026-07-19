//go:build e2e

package harness

import "encoding/json"

// ValidConfigJSON returns a minimal but valid config.json suitable for E2E tests.
// The config uses echo-based verify_command to avoid dependency on real build tools.
func ValidConfigJSON() string {
	cfg := map[string]interface{}{
		"project":         "golemic_e2e",
		"verify_command":  "echo 'Verification passed'",
		"label":           "ready-for-agent",
		"timeout_minutes": 30,
		"models": map[string]string{
			"dev":      "z-ai/glm-4.6",
			"reviewer": "deepseek/deepseek-v4-pro",
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return string(data)
}

// BrokenConfigs returns a map of deliberately broken config.json fixtures.
// Each key describes the failure mode, each value is the broken JSON content.
// Used to verify BR-004: preflight validation catches bad configs.
func BrokenConfigs() map[string]string {
	return map[string]string{
		"not-json":         `this is not valid json`,
		"missing_project":  `{"verify_command":"echo ok","label":"test"}`,
		"missing_verify":   `{"project":"test","label":"test"}`,
		"empty_object":     `{}`,
		"array_instead_of_object": `["project","value"]`,
		"null_project":     `{"project": null, "verify_command":"echo ok"}`,
		"invalid_project_number": `{"project": 123, "verify_command":"echo ok"}`,
	}
}
