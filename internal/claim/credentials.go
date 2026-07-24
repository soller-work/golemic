package claim

import (
	"encoding/json"
	"fmt"
	"os"

	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/preflight"
	"golemic/internal/repo"
)

// ResolveCredentials loads the dev-bot token and resolves the GitHub login.
func ResolveCredentials(executor preflight.Executor) (devLogin, devToken string, err error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("failed to get home directory: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("failed to get working directory: %w", err)
	}

	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		return "", "", err
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		return "", "", err
	}

	creds, err := credentials.NewLoader(homeDir).Load(cfg.Project)
	if err != nil {
		return "", "", err
	}

	token := creds.DevToken()
	userOut, err := executor.RunWithEnv(map[string]string{"GH_TOKEN": token}, "gh", "api", "user")
	if err != nil {
		return "", "", fmt.Errorf("gh api user failed: %w", err)
	}
	var ghUser struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(userOut), &ghUser); err != nil {
		return "", "", fmt.Errorf("gh api user: failed to parse response: %w", err)
	}
	if ghUser.Login == "" {
		return "", "", fmt.Errorf("gh api user: login field is empty")
	}
	return ghUser.Login, token, nil
}
