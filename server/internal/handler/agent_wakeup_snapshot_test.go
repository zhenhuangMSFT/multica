package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskResponsePreservesWakeupSnapshot(t *testing.T) {
	snapshot := `{"id":"wake","kind":"at","next_fire_at":"2099-01-01T10:09:00Z"}`
	for _, context := range []string{`{}`, `{"wakeup_id":"wake","wakeup_snapshot":` + snapshot + `}`} {
		response := taskToResponse(db.AgentTaskQueue{Context: []byte(context)}, "")
		if response.WakeupID == "" {
			if len(response.WakeupSnapshot) != 0 {
				t.Fatal("ordinary task acquired a wakeup snapshot")
			}
			continue
		}
		var expected, actual map[string]any
		if err := json.Unmarshal([]byte(snapshot), &expected); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(response.WakeupSnapshot, &actual); err != nil {
			t.Fatal(err)
		}
		if actual["next_fire_at"] != expected["next_fire_at"] || actual["id"] != expected["id"] {
			t.Fatalf("response changed snapshot: %s", response.WakeupSnapshot)
		}
	}
}
