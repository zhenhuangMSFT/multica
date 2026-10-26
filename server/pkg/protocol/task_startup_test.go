package protocol

import (
	"testing"
	"time"
)

func TestTaskStartupPhaseValidation(t *testing.T) {
	valid := TaskStartupPhase{Phase: "first_output_received", PhaseElapsedMS: 1000, TotalElapsedMS: 15000, ObservedAt: time.Now()}
	if !valid.Valid() {
		t.Fatal("valid phase rejected")
	}
	for _, change := range []func(*TaskStartupPhase){
		func(p *TaskStartupPhase) { p.Phase = "invented" },
		func(p *TaskStartupPhase) { p.PhaseElapsedMS = -1 },
		func(p *TaskStartupPhase) { p.TotalElapsedMS = 999 },
		func(p *TaskStartupPhase) { p.ObservedAt = time.Time{} },
	} {
		p := valid
		change(&p)
		if p.Valid() {
			t.Fatalf("invalid phase accepted: %+v", p)
		}
	}
}
