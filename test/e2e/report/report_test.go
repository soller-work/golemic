package main

import (
	"fmt"
	"strings"
	"testing"
)

// --- helpers -----------------------------------------------------------------

type fakeClient struct {
	fingerprints map[string]bool
	created      []fakeIssue
	listErr      error
	createErr    error
}

type fakeIssue struct {
	title string
	body  string
}

func (f *fakeClient) listFingerprints() (map[string]bool, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.fingerprints, nil
}

func (f *fakeClient) createIssue(title, body string) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, fakeIssue{title, body})
	return nil
}

func newFake(fps ...string) *fakeClient {
	m := map[string]bool{}
	for _, fp := range fps {
		m[fp] = true
	}
	return &fakeClient{fingerprints: m}
}

// --- parseFailures -----------------------------------------------------------

func TestParseFailures_emptyInput(t *testing.T) {
	_, err := parseFailures(strings.NewReader(""))
	if err == nil {
		t.Fatal("want error for empty input, got nil")
	}
}

func TestParseFailures_allPassing(t *testing.T) {
	input := `{"Action":"run","Test":"TestFoo","Package":"p"}
{"Action":"output","Test":"TestFoo","Output":"ok\n"}
{"Action":"pass","Test":"TestFoo","Package":"p","Elapsed":0.1}
{"Action":"pass","Package":"p","Elapsed":0.1}
`
	scenarios, err := parseFailures(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scenarios) != 0 {
		t.Errorf("want 0 failures, got %d", len(scenarios))
	}
}

func TestParseFailures_oneTopLevelFail(t *testing.T) {
	input := `{"Action":"run","Test":"TestFoo","Package":"p"}
{"Action":"output","Test":"TestFoo","Output":"--- FAIL: TestFoo\n"}
{"Action":"fail","Test":"TestFoo","Package":"p","Elapsed":0.1}
{"Action":"fail","Package":"p","Elapsed":0.1}
`
	scenarios, err := parseFailures(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("want 1 failure, got %d", len(scenarios))
	}
	if scenarios[0].Name != "TestFoo" {
		t.Errorf("want Name=TestFoo, got %q", scenarios[0].Name)
	}
}

func TestParseFailures_subtestFailDoesNotCountSeparately(t *testing.T) {
	// Only the top-level test fail event should produce a scenario.
	input := `{"Action":"run","Test":"TestFoo","Package":"p"}
{"Action":"run","Test":"TestFoo/Sub","Package":"p"}
{"Action":"fail","Test":"TestFoo/Sub","Package":"p","Elapsed":0.0}
{"Action":"fail","Test":"TestFoo","Package":"p","Elapsed":0.1}
`
	scenarios, err := parseFailures(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("want 1 failure (top-level only), got %d", len(scenarios))
	}
	if scenarios[0].Name != "TestFoo" {
		t.Errorf("want TestFoo, got %q", scenarios[0].Name)
	}
}

func TestParseFailures_malformedLinesSkipped(t *testing.T) {
	input := `not json at all
{"Action":"run","Test":"TestBar","Package":"p"}
{"Action":"fail","Test":"TestBar","Package":"p","Elapsed":0.1}
`
	scenarios, err := parseFailures(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scenarios) != 1 {
		t.Errorf("want 1 failure, got %d", len(scenarios))
	}
}

// --- classify ----------------------------------------------------------------

func TestClassify_panic(t *testing.T) {
	lines := []string{"panic: runtime error: index out of range\n", "goroutine 1 [running]:"}
	if got := classify(lines); got != classPanic {
		t.Errorf("want panic, got %q", got)
	}
}

func TestClassify_runtimeError(t *testing.T) {
	lines := []string{"runtime error: invalid memory address or nil pointer dereference"}
	if got := classify(lines); got != classPanic {
		t.Errorf("want panic, got %q", got)
	}
}

func TestClassify_setup(t *testing.T) {
	lines := []string{"--- SKIP: TestFoo (0.00s)", "missing prerequisite: sandbox not found"}
	if got := classify(lines); got != classSetup {
		t.Errorf("want setup, got %q", got)
	}
}

func TestClassify_assertion(t *testing.T) {
	lines := []string{"    report_test.go:42: want 0, got 1"}
	if got := classify(lines); got != classAssertion {
		t.Errorf("want assertion, got %q", got)
	}
}

func TestClassify_empty(t *testing.T) {
	if got := classify(nil); got != classAssertion {
		t.Errorf("want assertion for empty lines, got %q", got)
	}
}

// --- fingerprint -------------------------------------------------------------

func TestFingerprint_length(t *testing.T) {
	fp := fingerprint("TestFoo", classAssertion)
	if len(fp) != 12 {
		t.Errorf("want 12 hex chars, got %d: %q", len(fp), fp)
	}
}

func TestFingerprint_stable(t *testing.T) {
	fp1 := fingerprint("TestFoo", classAssertion)
	fp2 := fingerprint("TestFoo", classAssertion)
	if fp1 != fp2 {
		t.Errorf("fingerprint not stable: %q vs %q", fp1, fp2)
	}
}

func TestFingerprint_differentScenario(t *testing.T) {
	fp1 := fingerprint("TestFoo", classAssertion)
	fp2 := fingerprint("TestBar", classAssertion)
	if fp1 == fp2 {
		t.Errorf("different scenarios produced same fingerprint: %q", fp1)
	}
}

func TestFingerprint_differentClass(t *testing.T) {
	fp1 := fingerprint("TestFoo", classAssertion)
	fp2 := fingerprint("TestFoo", classPanic)
	if fp1 == fp2 {
		t.Errorf("different classes produced same fingerprint: %q", fp1)
	}
}

// --- report (dedup logic) ----------------------------------------------------

func TestReport_noFailures(t *testing.T) {
	client := newFake()
	var out strings.Builder
	if err := report(nil, client, "", nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.created) != 0 {
		t.Errorf("want 0 createIssue calls, got %d", len(client.created))
	}
}

func TestReport_createsIssue(t *testing.T) {
	s := failedScenario{
		Name:        "TestE2EHappyPath",
		Class:       classAssertion,
		Fingerprint: fingerprint("TestE2EHappyPath", classAssertion),
		Output:      []string{"--- FAIL: TestE2EHappyPath\n"},
	}
	client := newFake()
	var out strings.Builder
	if err := report([]failedScenario{s}, client, "https://github.com/owner/repo/actions/runs/1", nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.created) != 1 {
		t.Fatalf("want 1 createIssue call, got %d", len(client.created))
	}
	issue := client.created[0]
	if !strings.Contains(issue.title, "TestE2EHappyPath") {
		t.Errorf("title does not mention scenario name: %q", issue.title)
	}
	if !strings.Contains(issue.body, fingerprintPrefix+s.Fingerprint) {
		t.Errorf("body missing fingerprint line; body:\n%s", issue.body)
	}
	if !strings.Contains(issue.body, "https://github.com/owner/repo/actions/runs/1") {
		t.Errorf("body missing run link; body:\n%s", issue.body)
	}
}

func TestReport_dedup_suppressesExisting(t *testing.T) {
	fp := fingerprint("TestE2EHappyPath", classAssertion)
	s := failedScenario{
		Name:        "TestE2EHappyPath",
		Class:       classAssertion,
		Fingerprint: fp,
	}
	client := newFake(fp) // already known
	var out strings.Builder
	if err := report([]failedScenario{s}, client, "", nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.created) != 0 {
		t.Errorf("want 0 createIssue calls (suppressed by dedup), got %d", len(client.created))
	}
	if !strings.Contains(out.String(), "suppressed") {
		t.Errorf("want 'suppressed' in output, got: %s", out.String())
	}
}

func TestReport_dedup_onlyUnseenGetFiled(t *testing.T) {
	knownFP := fingerprint("TestKnown", classAssertion)
	newFP := fingerprint("TestNew", classPanic)

	scenarios := []failedScenario{
		{Name: "TestKnown", Class: classAssertion, Fingerprint: knownFP},
		{Name: "TestNew", Class: classPanic, Fingerprint: newFP},
	}
	client := newFake(knownFP)
	var out strings.Builder
	if err := report(scenarios, client, "", nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.created) != 1 {
		t.Fatalf("want 1 issue created (TestNew), got %d", len(client.created))
	}
	if !strings.Contains(client.created[0].title, "TestNew") {
		t.Errorf("wrong issue created: %q", client.created[0].title)
	}
}

func TestReport_apiError_returnsError(t *testing.T) {
	s := failedScenario{
		Name:        "TestFoo",
		Class:       classAssertion,
		Fingerprint: fingerprint("TestFoo", classAssertion),
	}
	client := &fakeClient{
		fingerprints: map[string]bool{},
		createErr:    fmt.Errorf("API unavailable"),
	}
	var out strings.Builder
	err := report([]failedScenario{s}, client, "", nil, &out)
	if err == nil {
		t.Fatal("want error from API failure, got nil")
	}
}

// --- buildIssueBody ----------------------------------------------------------

func TestBuildIssueBody_containsFingerprint(t *testing.T) {
	s := failedScenario{
		Name:        "TestFoo",
		Class:       classAssertion,
		Fingerprint: "aabbccddeeff",
		Output:      []string{"line1\n"},
	}
	body := buildIssueBody(s, "", nil)
	if !strings.Contains(body, "E2E-FINGERPRINT: aabbccddeeff") {
		t.Errorf("body missing fingerprint line:\n%s", body)
	}
}

func TestBuildIssueBody_redactsSecrets(t *testing.T) {
	s := failedScenario{
		Name:        "TestFoo",
		Class:       classAssertion,
		Fingerprint: "aabbccddeeff",
		Output:      []string{"token is supersecret123\n"},
	}
	body := buildIssueBody(s, "", []string{"supersecret123"})
	if strings.Contains(body, "supersecret123") {
		t.Errorf("body contains unredacted secret:\n%s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Errorf("body missing [REDACTED] marker:\n%s", body)
	}
}

func TestBuildIssueBody_trimLongLogs(t *testing.T) {
	bigLine := strings.Repeat("x", maxLogBytes+100)
	s := failedScenario{
		Name:        "TestFoo",
		Class:       classAssertion,
		Fingerprint: "aabbccddeeff",
		Output:      []string{bigLine},
	}
	body := buildIssueBody(s, "", nil)
	if !strings.Contains(body, "[trimmed]") {
		t.Errorf("body should contain trimmed marker for oversized logs:\n%s", body[:200])
	}
}

// --- trimLog -----------------------------------------------------------------

func TestTrimLog_belowLimit(t *testing.T) {
	s := strings.Repeat("a", 100)
	if got := trimLog(s, maxLogBytes); got != s {
		t.Errorf("short log should be unchanged")
	}
}

func TestTrimLog_aboveLimit(t *testing.T) {
	s := strings.Repeat("b", maxLogBytes+50)
	got := trimLog(s, maxLogBytes)
	if len(got) > maxLogBytes+20 { // allow for "[trimmed]" prefix
		t.Errorf("trimmed log too long: %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "...[trimmed]") {
		t.Errorf("want trimmed prefix, got: %s", got[:20])
	}
}

// --- redactSecrets -----------------------------------------------------------

func TestRedactSecrets_multipleTokens(t *testing.T) {
	s := "dev=abc123 reviewer=xyz789 ticket=tok456"
	got := redactSecrets(s, []string{"abc123", "xyz789", "tok456"})
	for _, secret := range []string{"abc123", "xyz789", "tok456"} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %q not redacted in: %q", secret, got)
		}
	}
}

func TestRedactSecrets_emptyToken(t *testing.T) {
	s := "no secrets here"
	got := redactSecrets(s, []string{""})
	if got != s {
		t.Errorf("empty token should not modify string")
	}
}
