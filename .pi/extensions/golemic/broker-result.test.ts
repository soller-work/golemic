import { describe, expect, it } from "vitest";
import {
  isBrokerErrorDetails,
  normalizeBrokerErrorResult,
  normalizeBrokerSuccessResult,
} from "./broker-result.ts";

describe("normalizeBrokerSuccessResult", () => {
  it("returns a text-part array for scalar content payloads and preserves details", () => {
    const payload = { ok: true, content: "text payload" };

    const result = normalizeBrokerSuccessResult(payload);

    expect(result).toEqual({
      content: [{ type: "text", text: "text payload" }],
      details: payload,
    });
  });

  it("keeps object payloads readable and preserves details", () => {
    const payload = { ok: true, spec: "issue body" };

    const result = normalizeBrokerSuccessResult(payload);

    expect(result.content).toEqual([{ type: "text", text: JSON.stringify(payload, null, 2) }]);
    expect(result.details).toBe(payload);
  });

  it("passes through already valid Pi tool results", () => {
    const payload = {
      content: [{ type: "text", text: "already valid" }],
      details: { raw: true },
    };

    expect(normalizeBrokerSuccessResult(payload)).toBe(payload);
  });
});

describe("normalizeBrokerErrorResult", () => {
  it("returns a valid error-shaped result for broker validation payloads", () => {
    const payload = { ok: false, code: "BAD_ARGUMENTS", message: "invalid query" };

    const result = normalizeBrokerErrorResult(payload);

    expect(result).toEqual({
      content: [{ type: "text", text: "invalid query" }],
      details: payload,
    });
    expect(isBrokerErrorDetails(result.details)).toBe(true);
  });

  it("returns a valid error-shaped result for transport failures", () => {
    const error = { code: "TRANSPORT_ERROR", message: "socket error: refused" };

    const result = normalizeBrokerErrorResult(error);

    expect(result).toEqual({
      content: [{ type: "text", text: "socket error: refused" }],
      details: error,
    });
    expect(isBrokerErrorDetails(result.details)).toBe(true);
  });
});
