# Cross-issue decisions in direct Chat

Status: proposed routing contract; only read-only source-context resolution is
implemented in this change. Subscriptions, delivery, approvals and the Chat UI
described below are NOT implemented or qualified. Do not enable a sole-notification
workflow on the strength of this prerequisite.

## Current capabilities and limits

Source inspection distinguishes a missing product contract from a missed event:

| Surface | Existing behavior |
| --- | --- |
| `server/cmd/multica/cmd_chat.go` | `history` and `thread` read the current task's conversation; neither sends messages. |
| `server/internal/handler/chat_history.go` | Resolves only `task.chat_session_id`; an issue task cannot use it to read its delegating chat. |
| `server/internal/service/issue_wakeup.go` | Persists issue-targeted wakeups and tasks; `parent_comment_id` is an issue comment, not a chat destination. |
| `server/internal/service/task.go` | Chat tasks persist actual Chat replies on completion. This capability is not an unsolicited cross-issue delivery API. |
| `server/internal/handler/chat.go` | Normal chat sends create a chat task. Session ownership, agent view and invocation access are separate gates. |
| `server/internal/attribution/attribution.go` | `delegated_from_task_id` and the originator retain delegation provenance. Accountable-human attribution is not consent. |

No existing supported configuration joins these mechanisms into the requested
subscription and approval flow. This does not establish that a backend cannot
send Chat messages: ordinary chat task completion already does so.

An additional limitation matters for approval design: a `chat_message` has a
`role`, session and input task, but no independently stored authenticated human
author. `SendDirectChatMessage` stamps direct-human task attribution while also
accepting an uploader actor type. A copied `yes`, a `role=user` row, or a task's
human originator must therefore never become an approval by inference.

## Implemented prerequisite: source context

`multica chat context [--source-message UUID] [--output json|table]` calls
`GET /api/chat/context`. A task token is required. The server follows at most 32
persisted delegation/automatic-retry edges from that token's task. Every task's
agent must remain in the selected workspace and every hop must retain the same
non-null originator. The starting task must still be dispatched or running and
belong to the token's agent.

The nearest Chat must be active, owned by that originator, and bound to the
source task's agent. The owner must still be a workspace member with view access
to that agent. IM-bound conversations are refused. No arbitrary session selector
is accepted, and manual-rerun or generic parent-task edges confer no provenance.

An exact source message, when supplied, must be a regular user-role message in
that source task's immutable input batch (including a retry's `chat_input_task_id`).
An older or newer turn in the same session is not interchangeable. The response
contains only `chat_session_id`, `source_task_id`, optional `source_message_id`,
`owner_id`, `delegation_hops`, and `authorization_granted: false`.

This is read-only provenance, not a send grant, subscription, approval, or
replacement for the existing transcript gates. It makes no schema changes and
does not change issue-only wakeup routing. A new server and CLI are required
before the command can run against an installed deployment.

## Proposed durable contract

Use explicit structured requests, not a classifier that mirrors issue comments.
Keep the ordinary issue execution/review log unchanged.

| Record | Required durable fields and invariants |
| --- | --- |
| Subscription | Workspace, creator/recipient human, verified direct session, source message/task, explicit bounded issue set (maximum 20), enabled flag, revision, creation/configuration actor. No workspace-wide wildcard or implicit scope growth. |
| Request | Workspace, subscription and revision, issue, source task/comment, explicit logical request key and revision, intended continuation agent, kind (`approval`, `information`, `blocker`), concise context, recommendation, immutable exact action scope, status. |
| Delivery | Request revision, destination session and owner, idempotency key, actual chat-message ID, attempt count, next retry, lease generation, last error, persisted timestamp and recipient acknowledgement timestamp. |
| Answer | Request/revision and displayed scope digest, authenticated human actor, exact Chat/session/message evidence, selected choice or information, accepted timestamp, resulting issue comment and continuation task ID. Immutable audit record. |

Do not add foreign keys/cascades. Use separate single-statement concurrent index
migrations for durable uniqueness and worker due-state indexes. Application
transactions own consistency and cleanup for issue, chat and workspace deletion.
Never store this protocol in free-form issue metadata or an agent's local files.

### Registration and request creation

The human confirms the bounded issue set in the initiating Chat. A task may
propose configuration and prove source identifiers with `chat context`; provenance
alone cannot silently enable the subscription. Configuration changes use revision
fencing and preserve the original human/session binding.

The issue's agent uses a structured request command, for example:

```text
multica issue decision create ISSUE --key maintenance-window \
  --request-file decision.json
```

The server binds the source issue/task from authenticated task context, checks
current membership and issue visibility for the recipient, and refuses unrelated
destinations. The file supplies kind, context, recommendation and exact scope,
not an author, human approval, or arbitrary destination session. Explicitly
registered requests are the event source; ordinary comments do not register
requests. Stable `(workspace, subscription, issue, key, revision)` uniqueness
deduplicates identical retries. Changed payload under the same key/revision
returns 409 rather than silently broadening approval.

Request creation and its outbox entry commit together. A worker leases outbox
rows, rechecks subscription revision/enabled state, recipient membership, issue
visibility and session/agent access, then persists the actual Chat message and
message ID with the delivery receipt transactionally. A durable unique request
revision prevents two workers from materializing two cards. WebSocket publication
is a hint after commit; reconnect/reload reads the committed message.

Offline browsers do not make delivery fail: distinguish queued, persisted in
Chat, acknowledged by the recipient, answered, cancelled, and delivery-failed.
The UI must not equate a persisted message with a human seeing it. Errors, retry
timing and a retry action must be visible on the issue; an issue comment succeeding
never marks Chat delivery successful. Retry transient transport errors with
bounded backoff and the same idempotency key; retain failed state after exhaustion.
Access failures fail closed without including private request content in notices.

### Answering and resuming

Both full-page and floating shared web/Desktop Chat render a structured request
card containing issue link, recommendation, scope and current state. Answer
controls bind a request ID and revision; no free-text parser manufactures consent.
A normal composer message remains discussion unless the human explicitly selects
a pending request to answer. A single selected request needs no disambiguation;
multiple possible requests prompt a chooser, never "latest issue wins."

A dedicated answer endpoint requires the authenticated member session, rejects
task tokens/agent actors and cross-owner/session requests, and captures the actor
server-side. An approve button confirms the displayed immutable scope digest.
The source-context endpoint or a quoted source comment cannot call this endpoint
on the owner's behalf. Information answers are not generalized action approvals.

Under request/subscription locks, recheck membership, issue/session access,
current revision, cancellation/expiry, and permission to invoke the intended
continuation agent. Atomically persist the answer, a member-attributed issue
comment recording request ID and exact scope, and one source-bound continuation
task. Reuse the task service's attribution, triage/readiness, runtime overlays
and dispatch admission; do not insert a bare task that bypasses those checks.
Finalize notifications only after commit.

A repeated identical answer returns the existing receipt. A conflicting answer,
stale scope, revoked access, deleted source or changed agent returns a visible
refusal without a comment or enqueue. Lock/CAS ownership plus durable
`request -> continuation_task_id` uniqueness prevents duplicate resumes after
timeouts/restarts. Existing pending-task dedup is not sufficient: its uniqueness
is not a durable one-answer/one-continuation ledger.

Disabling a subscription fences uncommitted delivery and suppresses later
forwarding without cancelling issue work. Previously delivered cards become
read-only while disabled; re-enabling does not replay answered/cancelled requests.
Request cancellation invalidates answer controls and undelivered outbox work.
Explicitly distinguish cancellation from disabling.

### Loop and access boundaries

Routing consumes only typed request events and never consumes its own delivery,
receipt or answer-comment events as new requests. Delivering an assistant card
must not enqueue a Chat task. The structured answer path enqueues only the named
continuation; it must not also run generic comment assignee/mention routing.
Repeated events, reconnects and worker restarts reuse durable IDs, not time-based
dedup caches. Quoted issue text is display-only untrusted data, not instructions
for routing or authorization.

Check visibility at registration, request capture, delivery, transcript projection
and answer time. Revocation after delivery requires redacting the structured
card's private payload on subsequent transcript reads, not just blocking the next
delivery. Render immutable provenance separately from the currently authorized
payload. Old clients must not receive private payload in an ordinary message
content field that bypasses the new projection gate.

## Qualification and rollout prerequisites

Full routing implementation needs the durable schema and the server/CLI/UI
contracts above. No new migration, subscription, Chat forwarding, agent dispatch,
production restart or deployment was performed by this prerequisite.

Before enabling a sole-notification workflow:

1. Obtain the operator's separate approval for additive durable schema migrations;
   prepare/review those migrations and execute them only in the approved scope.
   Production migration and cutover approval are distinct from isolated testing.
2. Implement and run isolated DB/handler/worker tests: two issues, separate
   decisions, duplicate/reordered capture, lease expiry, persisted-message/outbox
   failure boundaries, exhausted retries, offline delivery, disable/cancel,
   process restart, access revocation and no self-trigger loop.
3. Run real shared Chat UI browser fixtures (page and floating window): each
   request appears once, each authentic human answer is bound to the correct
   source, the exact scoped member comment appears once and the intended
   continuation is claimed once. Test ambiguity, forged/task-token answers,
   stale revisions, reload and post-delivery revocation. Component mocks or an
   issue-comment receipt alone do not qualify this path.
4. Review the combined tree with existing wakeup changes; keep this feature out
   of independent runtime-executor changes. Ship backend/schema first, then
   matching shared clients and CLI. Older issue-only workflows remain intact.
5. In an explicitly approved maintenance window and exclusive deployment path,
   back up data, deploy the reviewed artifacts, prove the initiating message's
   actual binding using the supported command, confirm only the selected issues,
   and qualify one owner-visible round trip before claiming reliable activation.

Until those steps complete, the initiating Chat is not the sole reliable
notification surface and issues remain the authoritative execution surface.
