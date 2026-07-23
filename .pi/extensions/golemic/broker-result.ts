const BROKER_ERROR_MARKER = Symbol.for("golemic.broker.error");

type TextContent = { type: "text"; text: string };

export type BrokerToolResult = {
  content: TextContent[];
  details: unknown;
};

export function normalizeBrokerSuccessResult(payload: unknown): BrokerToolResult {
  if (isPiToolResult(payload)) {
    return payload;
  }

  return {
    content: [{ type: "text", text: toReadableText(payload) }],
    details: markBrokerErrorDetails(payload),
  };
}

export function normalizeBrokerErrorResult(error: unknown): BrokerToolResult {
  return {
    content: [{ type: "text", text: toReadableText(error) }],
    details: markBrokerErrorDetails(error),
  };
}

export function isBrokerErrorDetails(details: unknown): boolean {
  return Boolean(details && typeof details === "object" && BROKER_ERROR_MARKER in details);
}

function isPiToolResult(payload: unknown): payload is BrokerToolResult {
  return Boolean(
    payload &&
      typeof payload === "object" &&
      "content" in payload &&
      Array.isArray((payload as { content?: unknown }).content),
  );
}

function markBrokerErrorDetails(details: unknown): unknown {
  if (!shouldMarkBrokerError(details)) {
    return details;
  }

  if (!details || typeof details !== "object") {
    return details;
  }

  try {
    Object.defineProperty(details, BROKER_ERROR_MARKER, {
      value: true,
      enumerable: false,
      configurable: true,
    });
  } catch {
    // If the broker payload is not extensible, preserve the raw payload.
  }

  return details;
}

function shouldMarkBrokerError(details: unknown): boolean {
  if (!details || typeof details !== "object") {
    return false;
  }

  const record = details as Record<string, unknown>;
  if (record.ok === false) {
    return true;
  }

  return record.code === "TRANSPORT_ERROR";
}

function toReadableText(value: unknown): string {
  if (typeof value === "string") {
    return value;
  }

  if (typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") {
    return String(value);
  }

  if (value == null) {
    return String(value);
  }

  if (Array.isArray(value)) {
    return JSON.stringify(value, null, 2);
  }

  if (typeof value === "object") {
    const record = value as Record<string, unknown>;
    if (typeof record.content === "string") {
      return record.content;
    }
    if (typeof record.message === "string") {
      return record.message;
    }

    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  }

  return String(value);
}
