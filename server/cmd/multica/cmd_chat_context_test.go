package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func TestChatSourceContextCommand(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const messageID = "11111111-1111-4111-8111-111111111111"
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/chat/context" ||
					r.URL.Query().Get("source_message_id") != messageID {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(status)
				if status == http.StatusOK {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"chat_session_id": "source-session", "source_task_id": "source-task",
						"source_message_id": messageID, "owner_id": "owner",
						"authorization_granted": false,
					})
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "source chat context is not available"})
				}
			}))
			defer server.Close()
			setCLITestServerEnv(t, server.URL)
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			cmd := &cobra.Command{}
			cmd.Flags().String("source-message", messageID, "")
			cmd.Flags().String("output", "json", "")
			err := runChatContext(cmd, nil)
			if (err != nil) != (status != http.StatusOK) {
				t.Fatalf("status %d: %v", status, err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}
