"use client";

import type { AgentTask } from "@multica/core/types/agent";
import { useT } from "../../i18n";
import { startupTiming } from "./startup-timing";

function duration(ms: number): string {
  return `${(ms / 1000).toFixed(1)}s`;
}

export function StartupTimingRow({ task }: { task: AgentTask }) {
  const { t } = useT("agents");
  const timing = startupTiming(task);
  return (
    <details className="shrink-0 text-caption">
      <summary className="cursor-pointer text-muted-foreground">
        {t(($) => $.transcript.startup.title)}
      </summary>
      <dl className="mt-2 grid grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-1 tabular-nums">
        <dt>{t(($) => $.transcript.startup.queue)}</dt>
        <dd>{timing.queueMs === undefined ? t(($) => $.transcript.startup.unknown) : duration(timing.queueMs)}</dd>
        <dt>{t(($) => $.transcript.startup.accepted_to_output)}</dt>
        <dd>{timing.acceptedToOutputMs === undefined ? t(($) => $.transcript.startup.unknown) : duration(timing.acceptedToOutputMs)}</dd>
        {timing.phases.map((phase) => (
          <div key={phase.phase} className="contents">
            <dt>{t(($) => $.transcript.startup.phases[phase.phase])}</dt>
            <dd>{duration(phase.total_elapsed_ms)}</dd>
          </div>
        ))}
      </dl>
      <p className="mt-2 text-micro text-muted-foreground">
        {t(($) => $.transcript.startup.clock_note)}
      </p>
    </details>
  );
}
