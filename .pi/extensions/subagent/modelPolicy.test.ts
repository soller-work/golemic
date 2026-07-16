/**
 * Tests for modelPolicy.ts pure functions.
 *
 * Run: npx vitest run modelPolicy.test.ts
 * (Requires vitest in the extension directory; if not available,
 *  these serve as documentation/contract tests for manual review.)
 */

import { describe, it, expect } from "vitest";
import {
	isFallbackWorthyTechnicalError,
	buildErrorSummary,
	resolveModelsToTry,
	buildAttemptArgs,
	classifyAttempt,
	computeFallbackStatus,
	runAttemptLoop,
} from "./modelPolicy.ts";
import type { AttemptResultInput, ModelAttempt, ModelChainResult } from "./modelPolicy.ts";

describe("isFallbackWorthyTechnicalError", () => {
	// ── Rate limit / quota ──
	it("detects rate limit", () => {
		expect(isFallbackWorthyTechnicalError("rate limit exceeded")).toBe(true);
	});
	it("detects quota", () => {
		expect(isFallbackWorthyTechnicalError("quota exceeded for this model")).toBe(true);
	});
	it("detects usage limit", () => {
		expect(isFallbackWorthyTechnicalError("usage limit reached")).toBe(true);
	});
	it("detects Claude Code hit-your-limit message", () => {
		expect(isFallbackWorthyTechnicalError("You've hit your limit · resets 9:10pm (Europe/Berlin)")).toBe(true);
	});
	it("detects reset-time quota message", () => {
		expect(isFallbackWorthyTechnicalError("limit reached, resets at 9:10pm")).toBe(true);
	});
	it("detects too many requests", () => {
		expect(isFallbackWorthyTechnicalError("too many requests, try again later")).toBe(true);
	});
	it("detects 429", () => {
		expect(isFallbackWorthyTechnicalError("HTTP 429 Too Many Requests")).toBe(true);
	});

	// ── Overloaded / temporarily unavailable ──
	it("detects overloaded", () => {
		expect(isFallbackWorthyTechnicalError("model is overloaded")).toBe(true);
	});
	it("detects temporarily unavailable", () => {
		expect(isFallbackWorthyTechnicalError("service temporarily unavailable")).toBe(true);
	});
	it("detects unavailable", () => {
		expect(isFallbackWorthyTechnicalError("model unavailable")).toBe(true);
	});
	it("detects capacity", () => {
		expect(isFallbackWorthyTechnicalError("at capacity, try later")).toBe(true);
	});

	// ── Timeout / network ──
	it("detects timeout", () => {
		expect(isFallbackWorthyTechnicalError("request timed out")).toBe(true);
	});
	it("detects ETIMEDOUT", () => {
		expect(isFallbackWorthyTechnicalError("ETIMEDOUT: connection failed")).toBe(true);
	});
	it("detects ECONNRESET", () => {
		expect(isFallbackWorthyTechnicalError("ECONNRESET")).toBe(true);
	});
	it("detects socket hang up", () => {
		expect(isFallbackWorthyTechnicalError("socket hang up")).toBe(true);
	});
	it("detects network reset", () => {
		expect(isFallbackWorthyTechnicalError("network reset by peer")).toBe(true);
	});
	it("detects ECONNREFUSED", () => {
		expect(isFallbackWorthyTechnicalError("ECONNREFUSED")).toBe(true);
	});

	// ── 5xx ──
	it("detects 500", () => {
		expect(isFallbackWorthyTechnicalError("HTTP 500 Internal Server Error")).toBe(true);
	});
	it("detects 502", () => {
		expect(isFallbackWorthyTechnicalError("502 Bad Gateway")).toBe(true);
	});
	it("detects 503", () => {
		expect(isFallbackWorthyTechnicalError("503 Service Unavailable")).toBe(true);
	});
	it("detects 504", () => {
		expect(isFallbackWorthyTechnicalError("504 Gateway Timeout")).toBe(true);
	});

	// ── Context too long (NOT fallback-worthy) ──
	it("rejects context length error", () => {
		expect(isFallbackWorthyTechnicalError("context length exceeded maximum")).toBe(false);
	});
	it("rejects context too long", () => {
		expect(isFallbackWorthyTechnicalError("context is too long for this model")).toBe(false);
	});
	it("rejects maximum context", () => {
		expect(isFallbackWorthyTechnicalError("maximum context window exceeded")).toBe(false);
	});
	it("rejects token limit in context", () => {
		expect(
			isFallbackWorthyTechnicalError("token limit exceeded: context is too long"),
		).toBe(false);
	});

	// ── Ordinary failures (NOT fallback-worthy) ──
	it("rejects ordinary non-technical failure", () => {
		expect(isFallbackWorthyTechnicalError("file not found: config.yaml")).toBe(false);
	});
	it("rejects compilation error", () => {
		expect(isFallbackWorthyTechnicalError("TypeError: undefined is not a function")).toBe(false);
	});
	it("rejects empty string", () => {
		expect(isFallbackWorthyTechnicalError("")).toBe(false);
	});

	// ── Mixed text: picks up fallback-worthy signals ──
	it("detects rate limit embedded in longer message", () => {
		expect(
			isFallbackWorthyTechnicalError("Error: API returned rate limit for model claude-sonnet-4"),
		).toBe(true);
	});
	it("detects 503 in multi-line stderr", () => {
		expect(
			isFallbackWorthyTechnicalError(
				"Error calling provider\nStatus: 503\nRetry later",
			),
		).toBe(true);
	});
});

describe("buildErrorSummary", () => {
	it("combines errorMessage and stderr", () => {
		const summary = buildErrorSummary("Something went wrong", "trace: connection refused\n", undefined);
		expect(summary).toContain("Something went wrong");
		expect(summary).toContain("trace: connection refused");
	});

	it("returns undefined when no error info available", () => {
		expect(buildErrorSummary(undefined, "", undefined)).toBeUndefined();
	});

	it("uses output when no errorMessage or stderr", () => {
		const summary = buildErrorSummary(undefined, "", "Final: failed");
		expect(summary).toBe("Final: failed");
	});

	it("truncates to 300 chars", () => {
		const long = "x".repeat(500);
		const summary = buildErrorSummary(long, undefined, undefined);
		expect(summary!.length).toBeLessThanOrEqual(300);
	});
});

// ──────────────────────────────────────────────────────────────────
// Execution seams — retry-loop orchestration
// ──────────────────────────────────────────────────────────────────

describe("resolveModelsToTry", () => {
	it("returns the single frontmatter model", () => {
		expect(resolveModelsToTry("c")).toEqual(["c"]);
	});

	it("returns [undefined] when no frontmatter model", () => {
		expect(resolveModelsToTry(undefined)).toEqual([undefined]);
	});

	it("splits a comma-separated frontmatter model into a fallback chain", () => {
		expect(
			resolveModelsToTry("openrouter/deepseek/deepseek-v4-pro, claude-bridge/claude-haiku-4-5"),
		).toEqual(["openrouter/deepseek/deepseek-v4-pro", "claude-bridge/claude-haiku-4-5"]);
	});

	it("trims whitespace and drops empty entries in a frontmatter chain", () => {
		expect(resolveModelsToTry(" a , , b ,")).toEqual(["a", "b"]);
	});

	it("dedupes repeated models in a frontmatter chain", () => {
		expect(resolveModelsToTry("a, b, a")).toEqual(["a", "b"]);
	});

	it("returns [undefined] when a frontmatter chain is only commas/whitespace", () => {
		expect(resolveModelsToTry(" , , ")).toEqual([undefined]);
	});
});

describe("buildAttemptArgs", () => {
	it("builds basic args without optional flags", () => {
		const args = buildAttemptArgs("dev", undefined, undefined, undefined, null, "do x");
		expect(args).toEqual(["--mode", "json", "-p", "--name", "subagent:dev", "Task: do x"]);
	});

	it("includes --session when sessionId provided", () => {
		const args = buildAttemptArgs("dev", "sess-1", undefined, undefined, null, "do x");
		expect(args).toContain("--session");
		expect(args).toContain("sess-1");
		expect(args).not.toContain("--name");
	});

	it("includes --model flag when model is specified", () => {
		const args = buildAttemptArgs("dev", undefined, "gpt-5", undefined, null, "do x");
		expect(args).toContain("--model");
		expect(args).toContain("gpt-5");
	});

	it("includes --tools when tools provided", () => {
		const args = buildAttemptArgs("dev", undefined, undefined, ["bash", "read"], null, "do x");
		expect(args).toContain("--tools");
		expect(args).toContain("bash,read");
	});

	it("includes --append-system-prompt when promptPath provided", () => {
		const args = buildAttemptArgs("dev", undefined, undefined, undefined, "/tmp/prompt.md", "do x");
		expect(args).toContain("--append-system-prompt");
		expect(args).toContain("/tmp/prompt.md");
	});

	it("first policy model passed via --model", () => {
		const args = buildAttemptArgs("dev", undefined, "first-model", undefined, null, "do x");
		const modelIdx = args.indexOf("--model");
		expect(modelIdx).toBeGreaterThan(-1);
		expect(args[modelIdx + 1]).toBe("first-model");
	});

	it("does not include --model when model is undefined", () => {
		const args = buildAttemptArgs("dev", undefined, undefined, undefined, null, "do x");
		expect(args).not.toContain("--model");
	});
});

describe("classifyAttempt", () => {
	it("marks rate-limited attempt as fallbackWorthy", () => {
		const a = classifyAttempt({ exitCode: 1, stderr: "rate limit exceeded" }, "m1");
		expect(a.exitCode).toBe(1);
		expect(a.fallbackWorthy).toBe(true);
		expect(a.model).toBe("m1");
	});

	it("marks overloaded attempt as fallbackWorthy", () => {
		const a = classifyAttempt({ exitCode: 1, errorMessage: "model overloaded" }, "m2");
		expect(a.fallbackWorthy).toBe(true);
	});

	it("does NOT mark context-too-long as fallbackWorthy", () => {
		const a = classifyAttempt({ exitCode: 1, errorMessage: "context length exceeded" }, "m3");
		expect(a.fallbackWorthy).toBe(false);
	});

	it("does NOT mark ordinary failure as fallbackWorthy", () => {
		const a = classifyAttempt({ exitCode: 1, stderr: "TypeError: foo" }, "m4");
		expect(a.fallbackWorthy).toBe(false);
	});

	it("marks successful attempt as not failed", () => {
		const a = classifyAttempt({ exitCode: 0, output: "done" }, "m5");
		expect(a.exitCode).toBe(0);
		expect(a.fallbackWorthy).toBe(false);
	});

	it("includes truncated reason for failures", () => {
		const a = classifyAttempt({ exitCode: 1, errorMessage: "ERR: " + "x".repeat(400) }, "m6");
		expect(a.reason).toBeDefined();
		expect(a.reason!.length).toBeLessThanOrEqual(300);
	});

	// ── exitCode 0 + stopReason semantics (reviewer bug fix) ──

	it("exitCode 0 with isFailed=true + rate-limit → fallbackWorthy=true", () => {
		const a = classifyAttempt(
			{ exitCode: 0, errorMessage: "rate limit exceeded", isFailed: true },
			"m1",
		);
		expect(a.fallbackWorthy).toBe(true);
		expect(a.exitCode).toBe(0);
	});

	it("exitCode 0 with isFailed=true + overloaded → fallbackWorthy=true", () => {
		const a = classifyAttempt(
			{ exitCode: 0, errorMessage: "model overloaded", isFailed: true },
			"m2",
		);
		expect(a.fallbackWorthy).toBe(true);
	});

	it("exitCode 0 with isFailed=true + 503 → fallbackWorthy=true", () => {
		const a = classifyAttempt(
			{ exitCode: 0, stderr: "HTTP 503 Service Unavailable", isFailed: true },
			"m3",
		);
		expect(a.fallbackWorthy).toBe(true);
	});

	it("exitCode 0 with isFailed=true + context-too-long → fallbackWorthy=false", () => {
		const a = classifyAttempt(
			{ exitCode: 0, errorMessage: "context length exceeded", isFailed: true },
			"m4",
		);
		expect(a.fallbackWorthy).toBe(false);
	});

	it("exitCode 0 with isFailed=true + ordinary error → fallbackWorthy=false", () => {
		const a = classifyAttempt(
			{ exitCode: 0, errorMessage: "TypeError: undefined is not a function", isFailed: true },
			"m5",
		);
		expect(a.fallbackWorthy).toBe(false);
	});

	it("exitCode 0 with stopReason=aborted + rate-limit text → fallbackWorthy=true (error text wins)", () => {
		// When stopReason is "aborted" but error text is rate-limit, fallback is still sensible
		const a = classifyAttempt(
			{ exitCode: 0, errorMessage: "aborted: rate limit exceeded", isFailed: true },
			"m6",
		);
		expect(a.fallbackWorthy).toBe(true);
	});

	it("exitCode 0 without isFailed → treated as success (backward compat)", () => {
		const a = classifyAttempt(
			{ exitCode: 0, errorMessage: "rate limit exceeded" },
			"m7",
		);
		expect(a.fallbackWorthy).toBe(false);
		expect(a.exitCode).toBe(0);
	});
});

describe("computeFallbackStatus", () => {
	it("returns not-applicable when no policy chain", () => {
		const attempts: ModelAttempt[] = [{ model: "a", exitCode: 0, fallbackWorthy: false }];
		expect(computeFallbackStatus(attempts, false)).toBe("not-applicable");
	});

	it("returns not-needed when first model succeeds", () => {
		const attempts: ModelAttempt[] = [{ model: "a", exitCode: 0, fallbackWorthy: false }];
		expect(computeFallbackStatus(attempts, true)).toBe("not-needed");
	});

	it("returns fallback-succeeded after retry", () => {
		const attempts: ModelAttempt[] = [
			{ model: "a", exitCode: 1, fallbackWorthy: true, reason: "rate limit" },
			{ model: "b", exitCode: 0, fallbackWorthy: false },
		];
		expect(computeFallbackStatus(attempts, true)).toBe("fallback-succeeded");
	});

	it("returns exhausted when all models fail fallback-worthily", () => {
		const attempts: ModelAttempt[] = [
			{ model: "a", exitCode: 1, fallbackWorthy: true, reason: "rate limit" },
			{ model: "b", exitCode: 1, fallbackWorthy: true, reason: "overloaded" },
		];
		expect(computeFallbackStatus(attempts, true)).toBe("exhausted");
	});

	it("returns not-needed when non-fallback failure on first model", () => {
		const attempts: ModelAttempt[] = [
			{ model: "a", exitCode: 1, fallbackWorthy: false, reason: "TypeError" },
		];
		expect(computeFallbackStatus(attempts, true)).toBe("not-needed");
	});

	// ── Semantic succeeded (bug fix: honour isFailed over exitCode) ──

	it("all attempts exitCode=0 but isFailed=true → exhausted", () => {
		const attempts: ModelAttempt[] = [
			{ model: "a", exitCode: 0, fallbackWorthy: true, succeeded: false, reason: "rate limit" },
			{ model: "b", exitCode: 0, fallbackWorthy: true, succeeded: false, reason: "overloaded" },
		];
		expect(computeFallbackStatus(attempts, true)).toBe("exhausted");
	});

	it("one semantic failure + one real success → fallback-succeeded", () => {
		const attempts: ModelAttempt[] = [
			{ model: "a", exitCode: 0, fallbackWorthy: true, succeeded: false, reason: "rate limit" },
			{ model: "b", exitCode: 0, fallbackWorthy: false, succeeded: true },
		];
		expect(computeFallbackStatus(attempts, true)).toBe("fallback-succeeded");
	});

	it("first attempt genuine success → not-needed", () => {
		const attempts: ModelAttempt[] = [
			{ model: "a", exitCode: 0, fallbackWorthy: false, succeeded: true },
		];
		expect(computeFallbackStatus(attempts, true)).toBe("not-needed");
	});

	it("succeeded falls back to exitCode === 0 when undefined", () => {
		// Backward compat: no succeeded field → derived from exitCode
		const attempts: ModelAttempt[] = [
			{ model: "a", exitCode: 0, fallbackWorthy: false },
		];
		expect(computeFallbackStatus(attempts, true)).toBe("not-needed");
	});

	it("succeeded=false overrides exitCode=0 for exhausted check", () => {
		const attempts: ModelAttempt[] = [
			{ model: "a", exitCode: 0, fallbackWorthy: true, succeeded: false, reason: "503" },
			{ model: "b", exitCode: 0, fallbackWorthy: true, succeeded: false, reason: "timeout" },
			{ model: "c", exitCode: 0, fallbackWorthy: true, succeeded: false, reason: "rate limit" },
		];
		expect(computeFallbackStatus(attempts, true)).toBe("exhausted");
	});
});

describe("runAttemptLoop", () => {
	it("stops on first success (no retry needed)", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			["a", "b"],
			async (m) => {
				calls.push(m);
				return { exitCode: 0, output: `ok-${m}` };
			},
		);
		expect(calls).toEqual(["a"]); // Only first model tried
		expect(result.successIndex).toBe(0);
		expect(result.modelAttempts).toHaveLength(1);
		expect(result.fallbackStatus).toBe("not-needed");
	});

	it("retries on fallback-worthy failure", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			["a", "b", "c"],
			async (m) => {
				calls.push(m);
				if (m === "a") return { exitCode: 1, stderr: "rate limit exceeded" };
				if (m === "b") return { exitCode: 1, stderr: "service unavailable" };
				return { exitCode: 0, output: "ok-c" };
			},
		);
		expect(calls).toEqual(["a", "b", "c"]);
		expect(result.successIndex).toBe(2);
		expect(result.modelAttempts).toHaveLength(3);
		expect(result.modelAttempts[0].fallbackWorthy).toBe(true);
		expect(result.modelAttempts[1].fallbackWorthy).toBe(true);
		expect(result.modelAttempts[2].fallbackWorthy).toBe(false);
		expect(result.fallbackStatus).toBe("fallback-succeeded");
		expect(result.finalModel).toBe("c");
	});

	it("does NOT retry on context-too-long failure", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			["a", "b"],
			async (m) => {
				calls.push(m);
				return { exitCode: 1, errorMessage: "context length exceeded" };
			},
		);
		expect(calls).toEqual(["a"]); // Stops after first
		expect(result.successIndex).toBe(-1);
		expect(result.modelAttempts).toHaveLength(1);
		expect(result.fallbackStatus).toBe("not-needed");
	});

	it("does NOT retry on ordinary non-technical failure", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			["a", "b"],
			async (m) => {
				calls.push(m);
				return { exitCode: 1, stderr: "file not found" };
			},
		);
		expect(calls).toEqual(["a"]);
		expect(result.successIndex).toBe(-1);
		expect(result.fallbackStatus).toBe("not-needed");
	});

	it("exhausts all models and reports exhausted", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			["a", "b"],
			async (m) => {
				calls.push(m);
				return { exitCode: 1, stderr: "rate limit exceeded" };
			},
		);
		expect(calls).toEqual(["a", "b"]);
		expect(result.successIndex).toBe(-1);
		expect(result.modelAttempts).toHaveLength(2);
		expect(result.modelAttempts[0].fallbackWorthy).toBe(true);
		expect(result.modelAttempts[1].fallbackWorthy).toBe(true);
		expect(result.fallbackStatus).toBe("exhausted");
	});

	it("preserves modelAttempts details in result", async () => {
		const result = await runAttemptLoop(
			["a", "b"],
			async (m) => {
				if (m === "a") return { exitCode: 1, stderr: "rate limit" };
				return { exitCode: 0, output: "ok" };
			},
		);
		expect(result.modelAttempts).toBeDefined();
		expect(result.modelAttempts[0].model).toBe("a");
		expect(result.modelAttempts[0].exitCode).toBe(1);
		expect(result.modelAttempts[0].reason).toBeDefined();
		expect(result.modelAttempts[1].model).toBe("b");
		expect(result.modelAttempts[1].exitCode).toBe(0);
		expect(result.finalModel).toBe("b");
		expect(result.fallbackStatus).toBe("fallback-succeeded");
	});

	it("handles single-element chain with success", async () => {
		const result = await runAttemptLoop(
			["a"],
			async () => ({ exitCode: 0, output: "ok" }),
			undefined,
			true,
		);
		expect(result.successIndex).toBe(0);
		expect(result.fallbackStatus).toBe("not-needed");
	});

	it("handles undefined models in chain", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			[undefined, "b"],
			async (m) => {
				calls.push(m);
				if (m === undefined) return { exitCode: 1, stderr: "rate limit" };
				return { exitCode: 0, output: `ok-${m}` };
			},
		);
		expect(calls).toEqual([undefined, "b"]);
		expect(result.finalModel).toBe("b");
		expect(result.fallbackStatus).toBe("fallback-succeeded");
	});

	it("respects abort signal mid-loop", async () => {
		const controller = new AbortController();
		const calls: (string | undefined)[] = [];
		const promise = runAttemptLoop(
			["a", "b", "c"],
			async (m) => {
				calls.push(m);
				if (m === "a") {
					controller.abort();
					return { exitCode: 1, stderr: "rate limit" };
				}
				return { exitCode: 0, output: "ok" };
			},
			controller.signal,
		);
		const result = await promise;
		expect(calls).toEqual(["a"]); // Stopped after abort
		expect(result.successIndex).toBe(-1);
	});

	it("retries when isFailed=true with rate-limit even if exitCode=0", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			["a", "b"],
			async (m) => {
				calls.push(m);
				if (m === "a") return { exitCode: 0, errorMessage: "rate limit exceeded", isFailed: true };
				return { exitCode: 0, output: "ok" };
			},
		);
		expect(calls).toEqual(["a", "b"]);
		expect(result.successIndex).toBe(1);
		expect(result.fallbackStatus).toBe("fallback-succeeded");
	});

	it("does NOT retry when isFailed=true with context-too-long", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			["a", "b"],
			async (m) => {
				calls.push(m);
				return { exitCode: 0, errorMessage: "context length exceeded", isFailed: true };
			},
		);
		expect(calls).toEqual(["a"]);
		expect(result.successIndex).toBe(-1);
	});

	it("does NOT retry when isFailed=true with ordinary error", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			["a", "b"],
			async (m) => {
				calls.push(m);
				return { exitCode: 0, errorMessage: "TypeError: x is not a function", isFailed: true };
			},
		);
		expect(calls).toEqual(["a"]);
		expect(result.successIndex).toBe(-1);
	});

	it("exhausted: all attempts exitCode=0, isFailed=true, fallback-worthy", async () => {
		const calls: (string | undefined)[] = [];
		const result = await runAttemptLoop(
			["a", "b"],
			async (m) => {
				calls.push(m);
				return { exitCode: 0, errorMessage: "rate limit exceeded", isFailed: true };
			},
		);
		expect(calls).toEqual(["a", "b"]);
		expect(result.successIndex).toBe(-1);
		expect(result.fallbackStatus).toBe("exhausted");
		expect(result.modelAttempts).toHaveLength(2);
		expect(result.modelAttempts[0].succeeded).toBe(false);
		expect(result.modelAttempts[1].succeeded).toBe(false);
	});
});