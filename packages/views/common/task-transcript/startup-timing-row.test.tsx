// @vitest-environment jsdom
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it } from "vitest";
import type { AgentTask } from "@multica/core/types/agent";
import { renderWithI18n } from "../../test/i18n";
import { StartupTimingRow } from "./startup-timing-row";

afterEach(cleanup);

const task: AgentTask = {
  id: "task", agent_id: "agent", runtime_id: "runtime", issue_id: "issue",
  status: "running", priority: 0, created_at: "2026-10-26T00:00:00Z",
  dispatched_at: "2026-10-26T00:00:02Z", started_at: "2026-10-26T00:00:03Z",
  completed_at: null, error: null, result: null,
};

it("shows queue wait and explicitly unknown old-daemon timing behind a disclosure", async () => {
  renderWithI18n(<StartupTimingRow task={task} />);
  await userEvent.click(screen.getByText("Startup timing"));
  expect(screen.getByText("Queue wait")).toBeVisible();
  expect(screen.getByText("2.0s")).toBeVisible();
  expect(screen.getByText("Not recorded")).toBeVisible();
  expect(screen.getByText(/synchronized clocks/)).toBeVisible();
});

it("renders updated phases without presenting telemetry as agent output", () => {
  const { rerender } = renderWithI18n(<StartupTimingRow task={task} />);
  rerender(<StartupTimingRow task={{
    ...task, result: { startup_timing: {
      dispatched_at: task.dispatched_at,
      phases: { environment_ready: {
        phase: "environment_ready", total_elapsed_ms: 1200, phase_elapsed_ms: 200,
        observed_at: "2026-10-26T00:00:03.200Z",
      } },
    } },
  }} />);
  expect(screen.getByText("Environment ready")).toBeInTheDocument();
  expect(screen.getByText("1.2s")).toBeInTheDocument();
});
