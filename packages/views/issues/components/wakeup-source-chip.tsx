"use client";

import { useCallback, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bell } from "lucide-react";
import { issueTasksOptions, issueTimelineOptions } from "@multica/core/issues";
import type { AgentTask, TimelineEntry } from "@multica/core/types";
import { useActorName } from "@multica/core/workspace/hooks";
import { useT } from "../../i18n";
import { useWakeupText } from "./wakeup-presentation";
import { wakeupActivityPreview, wakeupSnapshotPreview } from "./wakeup-activity";

function useRunWakeupSnapshot(task: AgentTask) {
  const { getActorName } = useActorName();
  const select = useCallback(
    (entries: TimelineEntry[]) => entries.find((entry) =>
      entry.action === "wakeup_triggered" && entry.details?.task_id === task.id),
    [task.id],
  );
  const { data: entry } = useQuery({
    ...issueTimelineOptions(task.issue_id),
    enabled: !!task.issue_id && !!task.wakeup_id && !task.wakeup_snapshot,
    select,
  });
  return task.wakeup_snapshot
    ? wakeupSnapshotPreview(task.wakeup_snapshot, getActorName)
    : (entry ? wakeupActivityPreview(entry, getActorName) : undefined);
}

/**
 * "Triggered by wakeup · <condition>" on a comment an agent posted from a run
 * that a wakeup started. Renders nothing for other comments.
 */
export function WakeupSourceChip({ issueId, taskId }: { issueId: string; taskId: string }) {
  const select = useCallback((tasks: AgentTask[]) => tasks.find((task) => task.id === taskId), [taskId]);
  const { data: task } = useQuery({ ...issueTasksOptions(issueId), select });
  return task?.wakeup_id ? <WakeupSourceChipLabel task={task} /> : null;
}

function WakeupSourceChipLabel({ task }: { task: AgentTask }) {
  const { t } = useT("issues");
  const text = useWakeupText();
  const snapshot = useRunWakeupSnapshot(task);
  const label = snapshot
    ? t(($) => $.execution_log.trigger_wakeup, { condition: text.trigger(snapshot) })
    : t(($) => $.wakeups.triggered_by_wakeup);
  return (
    <span
      className="inline-flex min-w-0 max-w-64 items-center gap-1 rounded-sm bg-muted px-1.5 py-0.5 text-micro text-muted-foreground"
      title={label}
    >
      <Bell className="size-3 shrink-0" aria-hidden="true" />
      <span className="truncate">{label}</span>
    </span>
  );
}

/**
 * A wakeup run's trigger label uses its immutable snapshot, never the rule's
 * current schedule. Older runs use their timeline snapshot when available.
 */
export function WakeupRunLabel({
  task,
  fallback,
  render,
}: {
  task: AgentTask;
  fallback: string;
  render: (label: string) => ReactNode;
}) {
  const { t } = useT("issues");
  const text = useWakeupText();
  const snapshot = useRunWakeupSnapshot(task);
  return render(snapshot ? t(($) => $.execution_log.trigger_wakeup, { condition: text.trigger(snapshot) }) : fallback);
}
