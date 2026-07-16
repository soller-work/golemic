/**
 * Model fallback classification and retry-loop orchestration
 * for the subagent extension.
 *
 * The fallback chain for an agent comes from its frontmatter `model`
 * field, which may list several comma-separated models, e.g.:
 *
 *   model: openrouter/deepseek/deepseek-v4-pro, claude-bridge/claude-haiku-4-5
 *
 * On a fallback-worthy technical failure the next model is tried.
 */

export interface ModelAttempt {
	model?: string;
	exitCode: number;
	fallbackWorthy: boolean;
	reason?: string;
	/** Semantic success (honours isFailed semantics from caller).
	 *  Falls back to exitCode === 0 for backward compat. */
	succeeded?: boolean;
}

export type FallbackStatus = "not-applicable" | "not-needed" | "fallback-succeeded" | "exhausted";

/**
 * Classify whether an error text represents a technical/availability
 * failure that is worth retrying with a different model.
 *
 * Returns true for: rate limits, quota, overloaded, timeout,
 * network errors, and 5xx server errors.
 *
 * Returns false for: context-too-long, ordinary non-technical failures.
 */
export function isFallbackWorthyTechnicalError(text: string): boolean {
	const lower = text.toLowerCase();

	// ── Explicitly excluded: context length / token limit ──
	if (/context.{0,20}(?:length|too long|exceed|limit)/i.test(lower)) return false;
	if (/maximum.{0,20}context/i.test(lower)) return false;
	if (
		/(?:token|prompt|input).{0,40}(?:limit|exceed|too.{0,10}(?:long|large|big))/i.test(lower) &&
		/(?:context|prompt|input|token).{0,20}(?:length|size|too|exceed)/i.test(lower)
	)
		return false;

	// ── Rate limit / quota ──
	if (/rate.{0,10}limit/i.test(lower)) return true;
	if (/\bquota\b/i.test(lower)) return true;
	if (/usage.{0,10}limit/i.test(lower)) return true;
	if (/hit.{0,20}(?:your|the).{0,20}limit/i.test(lower)) return true;
	if (/you(?:'|’|`)?ve.{0,20}hit.{0,20}limit/i.test(lower)) return true;
	if (/resets?\s+(?:at\s+)?\d/i.test(lower)) return true;
	if (/too many requests/i.test(lower)) return true;
	if (/\b429\b/.test(text)) return true;

	// ── Overloaded / temporarily unavailable ──
	if (/overload/i.test(lower)) return true;
	if (/temporarily unavailable/i.test(lower)) return true;
	if (/\bunavailable\b/i.test(lower)) return true;
	if (/\bcapacity\b/i.test(lower)) return true;

	// ── Timeout / network ──
	if (/time.?out/i.test(lower)) return true;
	if (/timed.?out/i.test(lower)) return true;
	if (/etimedout/i.test(lower)) return true;
	if (/econnreset/i.test(lower)) return true;
	if (/socket hang up/i.test(lower)) return true;
	if (/network reset/i.test(lower)) return true;
	if (/econnrefused/i.test(lower)) return true;

	// ── 5xx server errors ──
	if (/\b5\d{2}\b/.test(text)) return true;
	if (/server error/i.test(lower)) return true;
	if (/internal server error/i.test(lower)) return true;
	if (/bad gateway/i.test(lower)) return true;
	if (/service unavailable/i.test(lower)) return true;
	if (/gateway timeout/i.test(lower)) return true;

	return false;
}

/**
 * Build a short error summary from a result for use in ModelAttempt.reason.
 */
export function buildErrorSummary(
	errorMessage?: string,
	stderr?: string,
	output?: string,
): string | undefined {
	const parts: string[] = [];
	if (errorMessage) parts.push(errorMessage);
	if (stderr) {
		const trimmed = stderr.trim();
		if (trimmed) parts.push(trimmed);
	}
	if (parts.length === 0 && output) {
		parts.push(output);
	}
	const joined = parts.join(" | ").trim();
	return joined.length > 0 ? joined.slice(0, 300) : undefined;
}

// ──────────────────────────────────────────────────────────────────────
// Execution seams — retry-loop orchestration (pure, testable)
// ──────────────────────────────────────────────────────────────────────

/**
 * Determine which models to try in priority order.
 *
 * The agent frontmatter `model` field may list several models as a
 * comma-separated string (e.g. "openrouter/deepseek/deepseek-v4-pro,
 * claude-bridge/claude-haiku-4-5"); those are parsed into an ordered
 * fallback chain. No model at all → [undefined] (pi uses its default
 * model when --model is absent).
 */
export function resolveModelsToTry(agentModel: string | undefined): (string | undefined)[] {
	if (agentModel) {
		const models = agentModel
			.split(",")
			.map((m) => m.trim())
			.filter((m) => m.length > 0);
		if (models.length > 0) return [...new Set(models)];
	}
	return [undefined];
}

/**
 * Build the argv for a single subagent attempt.
 */
export function buildAttemptArgs(
	agentName: string,
	sessionId: string | undefined,
	model: string | undefined,
	tools: string[] | undefined,
	promptPath: string | null,
	task: string,
): string[] {
	const args: string[] = ["--mode", "json", "-p"];
	if (sessionId) args.push("--session", sessionId);
	else args.push("--name", `subagent:${agentName}`);
	if (model) args.push("--model", model);
	if (tools && tools.length > 0) args.push("--tools", tools.join(","));
	if (promptPath) args.push("--append-system-prompt", promptPath);
	args.push(`Task: ${task}`);
	return args;
}

/** Simplified result from a single attempt (no pi-ai imports). */
export interface AttemptResultInput {
	exitCode: number;
	errorMessage?: string;
	stderr?: string;
	output?: string;
	/**
	 * Override for failure classification. When provided, this is used
	 * instead of `exitCode !== 0` to decide whether the attempt failed.
	 * Callers should pass the same semantics as `isFailedResult()` in
	 * index.ts (which also checks stopReason).
	 */
	isFailed?: boolean;
}

/**
 * Classify a single attempt result into a ModelAttempt record.
 */
export function classifyAttempt(
	result: AttemptResultInput,
	model: string | undefined,
): ModelAttempt {
	const errorText = buildErrorSummary(result.errorMessage, result.stderr, result.output);
	const isFailed = result.isFailed ?? (result.exitCode !== 0);
	const fallbackWorthy = isFailed ? isFallbackWorthyTechnicalError(errorText ?? "") : false;
	return { model, exitCode: result.exitCode, fallbackWorthy, reason: errorText, succeeded: !isFailed };
}

/**
 * Compute the overall fallback status from all attempts.
 */
export function computeFallbackStatus(
	attempts: ModelAttempt[],
	hasFallbackChain: boolean,
): FallbackStatus {
	if (!hasFallbackChain) return "not-applicable";
	const hadFallback = attempts.some((a) => a.fallbackWorthy);
	const anySucceeded = attempts.some((a) => a.succeeded ?? (a.exitCode === 0));
	if (anySucceeded) {
		return attempts.length > 1 ? "fallback-succeeded" : "not-needed";
	}
	return hadFallback ? "exhausted" : "not-needed";
}

/** Return value from runAttemptLoop. */
export interface ModelChainResult {
	modelAttempts: ModelAttempt[];
	finalModel?: string;
	fallbackStatus: FallbackStatus;
	/** Index of the successful attempt, or -1 if none succeeded. */
	successIndex: number;
}

/**
 * Orchestrate a model fallback loop.
 *
 * @param models          Ordered list of model IDs to try.
 * @param runAttempt      Callback that executes one attempt and returns a
 *                        simple result (exitCode + error info).
 * @param signal          Optional AbortSignal to cancel mid-loop.
 * @param forceFallbackChain  Override for whether these models form a
 *                        fallback chain (default: models.length > 1).
 *                        Controls fallbackStatus semantics.
 */
export async function runAttemptLoop(
	models: (string | undefined)[],
	runAttempt: (model: string | undefined) => Promise<AttemptResultInput>,
	signal?: AbortSignal,
	forceFallbackChain?: boolean,
): Promise<ModelChainResult> {
	const hasFallbackChain = forceFallbackChain ?? (models.length > 1);
	const modelAttempts: ModelAttempt[] = [];

	for (let i = 0; i < models.length; i++) {
		if (signal?.aborted) break;

		const model = models[i];
		const isLast = i === models.length - 1;

		let attemptResult: AttemptResultInput;
		try {
			attemptResult = await runAttempt(model);
		} catch (e) {
			attemptResult = { exitCode: 1, errorMessage: String(e) };
		}

		const attempt = classifyAttempt(attemptResult, model);
		modelAttempts.push(attempt);

		const succeeded = attemptResult.isFailed !== undefined
			? !attemptResult.isFailed
			: attemptResult.exitCode === 0;

		if (succeeded) {
			// Success — stop here.
			const status = computeFallbackStatus(modelAttempts, hasFallbackChain);
			return { modelAttempts, finalModel: model, fallbackStatus: status, successIndex: i };
		}

		if (!attempt.fallbackWorthy || isLast) {
			// Non-fallback failure, or all models exhausted.
			const status = computeFallbackStatus(modelAttempts, hasFallbackChain);
			return { modelAttempts, finalModel: model, fallbackStatus: status, successIndex: -1 };
		}

		// Fallback-worthy — continue to next model.
	}

	// Exhausted (all models tried, all failed fallback-worthily).
	const status = computeFallbackStatus(modelAttempts, hasFallbackChain);
	return { modelAttempts, finalModel: undefined, fallbackStatus: status, successIndex: -1 };
}