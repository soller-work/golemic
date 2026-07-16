package runner

import (
	"golemic/internal/preflight"
	"golemic/internal/repo"
)

// resolveHostRepo determines the host repository root by delegating to repo.ResolveHostRepo.
// This handles the case where golemic is symlinked into a host repo.
func resolveHostRepo(exec preflight.Executor, cwd string) (string, error) {
	return repo.ResolveHostRepo(exec, cwd)
}
