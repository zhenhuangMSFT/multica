CREATE TABLE daemon_maintenance_admission (
    daemon_id text NOT NULL,
    state jsonb NOT NULL
);

-- A transaction-scoped shared lock covers the reservation and its commit.
-- VOLATILE gives the state read a fresh snapshot after an exclusive writer.
CREATE FUNCTION daemon_runtime_admission_open(runtime_id uuid)
RETURNS boolean LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    target text;
    current_target text;
    fence jsonb;
BEGIN
    -- Bind admission to the runtime row until the reservation commits, so a
    -- concurrent re-registration cannot move it behind a different gate.
    -- NOWAIT avoids inverting the existing agent/runtime delivery lock order;
    -- contention aborts this reservation transaction rather than bypassing it.
    SELECT daemon_id INTO target FROM agent_runtime WHERE id = runtime_id FOR SHARE NOWAIT;
    IF NOT FOUND THEN RETURN false; END IF;
    IF target IS NULL OR target = '' THEN RETURN true; END IF;
    PERFORM pg_advisory_xact_lock_shared(hashtextextended('maintenance:' || target, 0));
    SELECT daemon_id INTO current_target FROM agent_runtime WHERE id = runtime_id;
    IF NOT FOUND OR current_target IS DISTINCT FROM target THEN RETURN false; END IF;
    SELECT state INTO fence FROM daemon_maintenance_admission WHERE daemon_id = target;
    IF NOT FOUND THEN RETURN true; END IF;
    -- Corrupt or incomplete state never behaves like an open gate.
    RETURN fence->'version' = '1'::jsonb
       AND fence->'closed' = 'false'::jsonb
       AND fence->'uncertain' = 'false'::jsonb
       AND fence->'delivery'->'command'->>'daemon_id' = target
       AND fence->'delivery'->'command'->>'operation' = 'release'
       AND fence->'result'->>'code' = 'ok'
       AND fence->'result'->'delivery' = fence->'delivery'
       AND fence->'generation' = fence->'delivery'->'command'->'generation'
       AND fence->'generation' = fence->'result'->'status'->'generation'
       AND fence->'result'->'status'->'blocked' = 'false'::jsonb
       AND fence->'result'->'status'->'persistence_fault' = 'false'::jsonb
       AND fence->'result'->'status'->'recovery_required' = 'false'::jsonb
       AND fence->'result'->'status'->>'holder' = '';
END;
$$;
