-- name: RuntimeMaintenanceAdmissionOpen :one
SELECT COALESCE(daemon_runtime_admission_open(@runtime_id::uuid), false)::boolean AS admitted;
