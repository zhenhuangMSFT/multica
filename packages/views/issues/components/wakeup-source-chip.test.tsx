import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AgentTask, TimelineEntry, WakeupPreview } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { WakeupRunLabel, WakeupSourceChip } from "./wakeup-source-chip";

vi.mock("@multica/core/issues", () => ({
  issueTasksOptions: (issueId: string) => ({
    queryKey: ["tasks", issueId],
    queryFn: () => { throw new Error("Unexpected task fetch"); },
    staleTime: Infinity,
  }),
  issueTimelineOptions: (issueId: string) => ({
    queryKey: ["timeline", issueId],
    queryFn: async () => [],
    staleTime: Infinity,
  }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (_type: string, id: string) => id }),
}));
vi.mock("./wakeup-presentation", () => ({
  useWakeupText: () => ({
    trigger: (snapshot: WakeupPreview) => snapshot.kind === "event"
      ? [snapshot.filter_actor_name, snapshot.filter_agent_name, snapshot.filter_task_id].join(" · ")
      : snapshot.next_fire_at,
  }),
}));

function snapshot(time: string): WakeupPreview {
  return {
    id: "wake", issue_id: "issue", agent_id: "agent", agent_name: "",
    kind: "at", mode: "once", event_types: [], timezone: "UTC",
    filter_task_id: null, filter_agent_name: null,
    interval_seconds: null, cron_expression: null, next_fire_at: time,
  };
}

function run(id: string, time?: string): AgentTask {
  return {
    id, issue_id: "issue", agent_id: "agent", runtime_id: "runtime",
    status: "completed", priority: 0, created_at: "2099-01-01T00:00:00Z",
    dispatched_at: null, started_at: null, completed_at: null,
    result: null, error: null, wakeup_id: "wake",
    ...(time ? { wakeup_snapshot: snapshot(time) } : {}),
  };
}

describe("historical wakeup labels", () => {
  it("resolves the saved event filters without fetching the live rule", () => {
    const client = new QueryClient();
    const task = run("filtered");
    task.wakeup_snapshot = {
      ...snapshot(""), kind: "event", event_types: ["task.completed"],
      filter_actor_type: "member", filter_actor_id: "member",
      filter_agent_id: "source-agent", filter_task_id: "source-run",
    };
    client.setQueryData(["tasks", "issue"], [task]);
    renderWithI18n(
      <QueryClientProvider client={client}>
        <WakeupSourceChip issueId="issue" taskId="filtered" />
      </QueryClientProvider>,
    );
    expect(screen.getByTitle("Wakeup · member · source-agent · source-run")).toBeInTheDocument();
  });

  it("keeps each run's time through repeated rescheduling and a reload", () => {
    const tasks = [run("first", "10:09"), run("second", "10:20"), run("third", "10:47")];
    const client = new QueryClient();
    client.setQueryData(["tasks", "issue"], tasks);
    const view = () => (
      <QueryClientProvider client={client}>
        {tasks.map((task) => <WakeupSourceChip key={task.id} issueId="issue" taskId={task.id} />)}
      </QueryClientProvider>
    );
    const rendered = renderWithI18n(view());
    const expectTimes = () => {
      for (const time of ["10:09", "10:20", "10:47"]) {
        expect(screen.getByTitle(`Wakeup · ${time}`)).toBeInTheDocument();
      }
    };
    expectTimes();
    for (const time of ["10:52", "10:57"]) {
      client.setQueryData(["issue-wakeups", "workspace", "issue"], [{ ...snapshot(time), enabled: true }]);
      rendered.rerender(view());
      expectTimes();
      expect(screen.queryByText(`Wakeup · ${time}`)).not.toBeInTheDocument();
    }
    rendered.unmount();
    client.clear();
    client.setQueryData(["tasks", "issue"], JSON.parse(JSON.stringify(tasks)));
    renderWithI18n(view());
    expectTimes();
  });

  it("uses snapshots in run lists and never invents a time for older runs", () => {
    const client = new QueryClient();
    client.setQueryData(["timeline", "issue"], []);
    renderWithI18n(
      <QueryClientProvider client={client}>
        <WakeupRunLabel task={run("new", "10:09")} fallback="Wakeup" render={(label) => <span>{label}</span>} />
        <WakeupRunLabel task={run("old")} fallback="Wakeup" render={(label) => <span>{label}</span>} />
      </QueryClientProvider>,
    );
    expect(screen.getByText("Wakeup · 10:09")).toBeInTheDocument();
    expect(screen.getByText("Wakeup", { exact: true })).toBeInTheDocument();
  });

  it("omits chips for ordinary runs and uses a generic chip for pre-snapshot wakeups", () => {
    const client = new QueryClient();
    client.setQueryData(["timeline", "issue"], []);
    client.setQueryData(["tasks", "issue"], [run("old"), { ...run("ordinary"), wakeup_id: undefined }]);
    renderWithI18n(
      <QueryClientProvider client={client}>
        <WakeupSourceChip issueId="issue" taskId="old" />
        <WakeupSourceChip issueId="issue" taskId="ordinary" />
      </QueryClientProvider>,
    );
    expect(screen.getAllByTitle("Triggered by wakeup")).toHaveLength(1);
  });

  it("preserves already-recorded historical times on pre-upgrade runs", () => {
    const client = new QueryClient();
    client.setQueryData(["tasks", "issue"], [run("old")]);
    const entry: TimelineEntry = {
      type: "activity", id: "trigger", actor_type: "system", actor_id: "",
      action: "wakeup_triggered", created_at: "2099-01-01T10:09:00Z",
      details: { task_id: "old", wakeup: { kind: "at", next_fire_at: "10:09" } },
    };
    client.setQueryData(["timeline", "issue"], [entry]);
    client.setQueryData(["issue-wakeups", "workspace", "issue"], [{ ...snapshot("10:47"), enabled: true }]);
    renderWithI18n(
      <QueryClientProvider client={client}>
        <WakeupSourceChip issueId="issue" taskId="old" />
      </QueryClientProvider>,
    );
    expect(screen.getByTitle("Wakeup · 10:09")).toBeInTheDocument();
    expect(screen.queryByText("Wakeup · 10:47")).not.toBeInTheDocument();
  });
});
