package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	fingerprintPrefix = "E2E-FINGERPRINT: "
	failureLabel      = "e2e-failure"
	maxRetries        = 3
)

// issueClient abstracts GitHub issue operations so the reporter logic can be
// tested without network access.
type issueClient interface {
	listFingerprints() (map[string]bool, error)
	createIssue(title, body string) error
}

type githubClient struct {
	token string
	repo  string
}

func newGitHubClient(token, repo string) *githubClient {
	return &githubClient{token: token, repo: repo}
}

type ghIssue struct {
	Body string `json:"body"`
}

func (c *githubClient) listFingerprints() (map[string]bool, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues?labels=%s&state=open&per_page=100",
		c.repo, failureLabel)
	var issues []ghIssue
	if err := c.doWithRetry("GET", url, nil, &issues); err != nil {
		return nil, err
	}

	fps := map[string]bool{}
	for _, iss := range issues {
		for _, line := range strings.Split(iss.Body, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, fingerprintPrefix) {
				fp := strings.TrimSpace(strings.TrimPrefix(line, fingerprintPrefix))
				fps[fp] = true
			}
		}
	}
	return fps, nil
}

func (c *githubClient) createIssue(title, body string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues", c.repo)
	payload := map[string]interface{}{
		"title":  title,
		"body":   body,
		"labels": []string{failureLabel},
	}
	return c.doWithRetry("POST", url, payload, nil)
}

func (c *githubClient) doWithRetry(method, url string, payload, out interface{}) error {
	var bodyBytes []byte
	if payload != nil {
		var err error
		bodyBytes, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
		err := c.doOnce(method, url, bodyBytes, out)
		if err == nil {
			return nil
		}
		if isRetryable(err) {
			lastErr = err
			continue
		}
		return err
	}
	return fmt.Errorf("after %d retries: %w", maxRetries, lastErr)
}

type retryableErr struct{ err error }

func (e *retryableErr) Error() string { return e.err.Error() }
func (e *retryableErr) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	_, ok := err.(*retryableErr)
	return ok
}

func (c *githubClient) doOnce(method, url string, bodyBytes []byte, out interface{}) error {
	var reqBody io.Reader
	if bodyBytes != nil {
		reqBody = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return &retryableErr{fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)}
	}
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, url, string(respBody))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
