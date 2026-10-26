package protocol

import "time"

// TaskStartupPhase is diagnostic data, not a task-state transition.
type TaskStartupPhase struct {
	Phase          string    `json:"phase"`
	PhaseElapsedMS int64     `json:"phase_elapsed_ms"`
	TotalElapsedMS int64     `json:"total_elapsed_ms"`
	ObservedAt     time.Time `json:"observed_at"`
}

func (p TaskStartupPhase) Valid() bool {
	if p.ObservedAt.IsZero() || p.PhaseElapsedMS < 0 || p.TotalElapsedMS < p.PhaseElapsedMS {
		return false
	}
	switch p.Phase {
	case "claimed", "prepare_started", "skills_ready", "environment_ready",
		"runtime_started", "first_output_received", "first_tool_use", "turn_completed", "finished":
		return true
	default:
		return false
	}
}
