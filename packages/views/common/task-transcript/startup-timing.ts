import type { AgentTask } from "@multica/core/types/agent";
import {
  TASK_STARTUP_PHASES,
  TaskStartupPhaseSchema,
  TaskStartupTimingResultSchema,
} from "@multica/core/api/schemas";

function interval(start: string | null, end: string | null): number | undefined {
  if (!start || !end) return undefined;
  const elapsed = Date.parse(end) - Date.parse(start);
  return Number.isFinite(elapsed) && elapsed >= 0 ? elapsed : undefined;
}

function claimTimestamp(value: string): string | undefined {
  const match = /^(.+T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(value);
  if (!match) return undefined;
  const seconds = Date.parse(`${match[1]}${match[3]}`);
  // Date.parse alone truncates PostgreSQL's microseconds and can alias two claims.
  return Number.isFinite(seconds) ? `${seconds}:${(match[2] ?? "").padEnd(9, "0")}` : undefined;
}

export function startupTiming(task: Pick<AgentTask, "created_at" | "dispatched_at" | "result">) {
  const timing = TaskStartupTimingResultSchema.safeParse(task.result);
  const claim = task.dispatched_at ? claimTimestamp(task.dispatched_at) : undefined;
  const current = timing.success && claim !== undefined &&
    claimTimestamp(timing.data.startup_timing.dispatched_at) === claim
    ? timing.data.startup_timing : undefined;
  const phases = TASK_STARTUP_PHASES.flatMap((name) => {
    const parsed = TaskStartupPhaseSchema.safeParse(current?.phases[name]);
    return parsed.success && parsed.data.phase === name ? [parsed.data] : [];
  });
  const firstOutput = phases.find((phase) => phase.phase === "first_output_received");
  return {
    phases,
    queueMs: interval(task.created_at, task.dispatched_at),
    acceptedToOutputMs: firstOutput ? interval(task.created_at, firstOutput.observed_at) : undefined,
  };
}
