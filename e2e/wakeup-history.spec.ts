import "./env";

import { expect, test } from "@playwright/test";
import pg from "pg";
import { createTestApi } from "./helpers";

test.use({ timezoneId: "UTC", locale: "en-US" });

test("wakeup replies keep their original trigger after rescheduling and reload", async ({ page }) => {
  test.setTimeout(240_000);
  const api = await createTestApi();
  const db = new pg.Client(process.env.DATABASE_URL);
  await db.connect();
  let runtimeId: string | undefined;
  let agentId: string | undefined;
  try {
    const workspace = (await api.getWorkspaces())[0]!;
    const user = await db.query<{ id: string }>(`SELECT id FROM "user" WHERE email = $1`, [api.getEmail()]);
    const runtime = await db.query<{ id: string }>(
      `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
       VALUES ($1, 'Wakeup history fixture', 'cloud', 'e2e_wakeups', 'online', 'E2E fixture', '{}'::jsonb, $2, now()) RETURNING id`,
      [workspace.id, user.rows[0]!.id],
    );
    runtimeId = runtime.rows[0]!.id;
    const agent = await db.query<{ id: string }>(
      `INSERT INTO agent (workspace_id, name, description, instructions, runtime_mode, runtime_config, runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id)
       VALUES ($1, 'Snapshot Agent', '', '', 'cloud', '{}'::jsonb, $2, 'private', 'private', 1, $3) RETURNING id`,
      [workspace.id, runtimeId, user.rows[0]!.id],
    );
    agentId = agent.rows[0]!.id;
    const issue = await api.createIssue("Keep wakeup history stable", { status: "in_progress" });
    const original = await api.createComment(issue.id, "Original scheduling context");
    const base = process.env.NEXT_PUBLIC_API_URL!;
    const send = async (path: string, method = "GET", data?: unknown, token = api.getToken()) => {
      const response = await page.request.fetch(`${base}${path}`, {
        method,
        headers: { Authorization: `Bearer ${token}`, "X-Workspace-ID": workspace.id },
        data,
      });
      expect(response.ok(), `${method} ${path}: ${response.status()}`).toBe(true);
      return response;
    };
    const rulesPath = `/api/issues/${issue.id}/wakeups`;
    const input = {
      agent_id: agentId, kind: "at", parent_comment_id: original.id,
      instruction: "Read current threads and report progress.", timezone: "UTC",
    };
    let rule = await (await send(rulesPath, "POST", { ...input, after_seconds: 300 })).json();
    const rulePath = `${rulesPath}/${rule.id}`;
    let current = await api.createComment(issue.id, "Post progress in this current thread");

    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token!);
      localStorage.setItem("multica:chat:isOpen", "false");
    }, api.getToken());
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`/${workspace.slug}/issues/${issue.identifier}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByRole("button", { name: /Show wakeups$/ })).toBeVisible({ timeout: 60_000 });
    rule = await (await send(rulePath, "PUT", { ...input, after_seconds: 1 })).json();

    const history: { taskId: string; replyId: string; scheduled: string; label: string }[] = [];
    for (let index = 0; index < 3; index++) {
      if (index > 0) {
        rule = await (await send(rulePath, "PUT", {
          ...input, at: new Date(Date.now() + index * 3_600_000).toISOString(),
        })).json();
        current = await api.createComment(issue.id, `New progress thread ${index + 1}`);
        await send(`${rulePath}/trigger`, "POST");
      }
      // The first run is dispatched by the real scheduler. Later runs use the
      // member's Run now endpoint so distinct future schedules need no clock warp.
      await expect.poll(async () => {
        const result = await db.query<{ n: number }>(
          `SELECT count(*)::int AS n FROM agent_task_queue WHERE context->>'wakeup_id' = $1`, [rule.id],
        );
        return result.rows[0]!.n;
      }, { timeout: 90_000, intervals: [1_000] }).toBe(index + 1);
      const claimed = await (await send(`/api/daemon/runtimes/${runtimeId}/tasks/claim`, "POST", {})).json();
      const task = claimed.task;
      expect(task).toBeTruthy();
      expect(task.wakeup_id).toBe(rule.id);
      expect(task.trigger_comment_id).toBe(original.id);
      expect(Date.parse(task.wakeup_snapshot.next_fire_at)).toBe(Math.floor(Date.parse(rule.next_fire_at) / 1000) * 1000);
      await send(`/api/daemon/tasks/${task.id}/start`, "POST", {});
      // A fixture runtime follows the prompt policy; no installed agent or
      // external model is executed by this browser regression.
      const reply = await (await send(`/api/issues/${issue.id}/comments`, "POST", {
        content: `Wakeup progress ${index + 1}`, parent_id: current.id,
      }, task.auth_token)).json();
      expect(reply.author_type).toBe("agent");
      expect(reply.parent_id).toBe(current.id);
      expect(reply.source_task_id).toBe(task.id);
      await send(`/api/daemon/tasks/${task.id}/complete`, "POST", { output: "Fixture complete" });
      await page.reload({ waitUntil: "domcontentloaded" });
      const chip = page.locator(`#comment-${reply.id}`).getByTitle(/^Wakeup ·/);
      await expect(chip).toBeVisible({ timeout: 30_000 });
      const time = new Intl.DateTimeFormat("en", { timeZone: "UTC", hour: "2-digit", minute: "2-digit" })
        .format(new Date(rule.next_fire_at));
      await expect(chip).toHaveAttribute("title", new RegExp(time.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
      history.push({ taskId: task.id, replyId: reply.id, scheduled: task.wakeup_snapshot.next_fire_at, label: (await chip.getAttribute("title"))! });
    }

    for (const hours of [4, 5]) {
      rule = await (await send(rulePath, "PUT", {
        ...input, at: new Date(Date.now() + hours * 3_600_000).toISOString(),
      })).json();
      await page.reload({ waitUntil: "domcontentloaded" });
      const time = new Intl.DateTimeFormat("en", { timeZone: "UTC", hour: "2-digit", minute: "2-digit" })
        .format(new Date(rule.next_fire_at));
      await expect(page.getByRole("button", { name: /Show wakeups$/ })).toContainText(time, { timeout: 30_000 });
      for (const prior of history) {
        await expect(page.locator(`#comment-${prior.replyId}`).getByTitle(prior.label, { exact: true })).toBeVisible();
        const persisted = await db.query<{ scheduled: string }>(
          `SELECT context->'wakeup_snapshot'->>'next_fire_at' AS scheduled FROM agent_task_queue WHERE id = $1`, [prior.taskId],
        );
        expect(persisted.rows[0]!.scheduled).toBe(prior.scheduled);
      }
    }
  } finally {
    await api.cleanup();
    if (agentId) {
      await db.query(`DELETE FROM agent_task_queue WHERE agent_id = $1`, [agentId]);
      await db.query(`DELETE FROM agent WHERE id = $1`, [agentId]);
    }
    if (runtimeId) await db.query(`DELETE FROM agent_runtime WHERE id = $1`, [runtimeId]);
    await db.end();
  }
});
