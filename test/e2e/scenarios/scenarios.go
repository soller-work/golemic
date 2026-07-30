// Package scenarios holds the scenario name constants shared by the detagent
// binary and the E2E test harness. Names must match the E2E-SCENARIO marker
// written into sandbox issue bodies by the harness.
package scenarios

const (
	HappyPath           = "happy_path"
	ReviewerRejectsOnce = "reviewer_rejects_once"
	DevVerifyFails      = "dev_verify_fails"
	Collision           = "collision"
	Timeout             = "timeout"
)
