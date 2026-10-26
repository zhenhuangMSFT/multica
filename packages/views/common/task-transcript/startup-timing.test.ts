// @vitest-environment node
import { describe, expect, it } from "vitest";
import { startupTiming } from "./startup-timing";

const task = {
  created_at: "2026-10-26T00:00:00Z",
  dispatched_at: "2026-10-26T00:00:02Z",
  result: {
    output: "unchanged",
    startup_timing: {
      dispatched_at: "2026-10-26T00:00:02Z",
      phases: {
        claimed: { phase: "claimed", phase_elapsed_ms: 0, total_elapsed_ms: 0, observed_at: "2026-10-26T00:00:02Z" },
        first_output_received: {
          phase: "first_output_received", phase_elapsed_ms: 13000, total_elapsed_ms: 15000,
          observed_at: "2026-10-26T00:00:17Z",
        },
      },
    },
  },
};

describe("startupTiming", () => {
  it("separates queue wait, first output and local phase timing", () => {
    const timing = startupTiming(task);
    expect(timing.queueMs).toBe(2000);
    expect(timing.acceptedToOutputMs).toBe(17000);
    expect(timing.phases.map((p) => p.phase)).toEqual(["claimed", "first_output_received"]);
    expect(timing.phases[1]?.total_elapsed_ms).toBe(15000);
  });

  it.each([null, {}, [], { startup_timing: null }])("keeps legacy or malformed timing unknown: %j", (result) => {
    expect(startupTiming({ ...task, result })).toEqual({
      phases: [], queueMs: 2000, acceptedToOutputMs: undefined,
    });
  });

  it("does not reuse a previous claim's timings after retry", () => {
    expect(startupTiming({ ...task, dispatched_at: "2026-10-26T00:01:00Z" }).phases).toEqual([]);
  });

  it("fences retries one microsecond apart without rejecting equivalent timestamps", () => {
    const result = structuredClone(task.result);
    result.startup_timing.dispatched_at = "2026-10-26T00:00:02.123456Z";
    expect(startupTiming({ ...task, result, dispatched_at: "2026-10-26T00:00:02.123457Z" }).phases).toEqual([]);
    expect(startupTiming({ ...task, result, dispatched_at: "2026-10-26T01:00:02.123456000+01:00" }).phases).toHaveLength(2);
  });

  it("ignores malformed, mismatched and future phases individually", () => {
    const result = structuredClone(task.result);
    Object.assign(result.startup_timing.phases, {
      claimed: { phase: "claimed", phase_elapsed_ms: -1, total_elapsed_ms: 0, observed_at: "invalid" },
      environment_ready: { phase: "skills_ready", phase_elapsed_ms: 1, total_elapsed_ms: 2, observed_at: "2026-10-26T00:00:03Z" },
      future_phase: { phase: "future_phase" },
    });
    expect(startupTiming({ ...task, result }).phases.map((p) => p.phase)).toEqual(["first_output_received"]);
  });

  it("does not render negative cross-clock or queue intervals as zero", () => {
    const result = structuredClone(task.result);
    result.startup_timing.phases.first_output_received.observed_at = "2026-10-25T23:59:59Z";
    const timing = startupTiming({ ...task, created_at: "2026-10-26T00:00:03Z", result });
    expect(timing.acceptedToOutputMs).toBeUndefined();
    expect(timing.queueMs).toBeUndefined();
  });
});
