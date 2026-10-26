-- name: RecordTaskStartupPhase :execrows
UPDATE agent_task_queue
SET result = COALESCE(result, '{}'::jsonb) || jsonb_build_object(
    'startup_timing', jsonb_build_object(
        'dispatched_at', sqlc.arg('claim')::text,
        'phases',
        jsonb_build_object(sqlc.arg('phase')::text, sqlc.arg('sample')::jsonb) ||
        CASE WHEN result #>> '{startup_timing,dispatched_at}' = sqlc.arg('claim')::text
            THEN COALESCE(result #> '{startup_timing,phases}', '{}'::jsonb)
            ELSE '{}'::jsonb END
    )
)
WHERE id = sqlc.arg('id')
  AND dispatched_at = sqlc.arg('dispatched_at')
  AND runtime_id = sqlc.arg('runtime_id');
