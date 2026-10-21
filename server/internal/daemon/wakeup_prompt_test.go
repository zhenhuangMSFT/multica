package daemon

import (
	"strings"
	"testing"
)

func TestWakeupPromptUsesCurrentDeliveryContext(t *testing.T) {
	p := BuildPrompt(Task{IssueID: "issue", WakeupID: "wake", TriggerCommentID: "original", TriggerCommentContent: "old instruction", HandoffNote: "task.completed run-x; check its result"}, "codex")
	for _, want := range []string{"[WAKEUP]", "check its result", "saved delivery context is comment original", "latest relevant user message", "explicit thread pin", "--parent <selected-comment-id>", "wakeup disable issue wake", "--roots-only"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, stale := range []string{"[NEW COMMENT]", "old instruction", "--parent original", "This is the original delivery thread"} {
		if strings.Contains(p, stale) {
			t.Errorf("replayed stale delivery instruction %q", stale)
		}
	}
}

func TestWakeupPromptPreservesExplicitUserPin(t *testing.T) {
	instruction := "The user explicitly pinned delivery to progress-thread, even when newer messages arrive."
	p := BuildPrompt(Task{IssueID: "issue", WakeupID: "wake", TriggerCommentID: "original", HandoffNote: instruction}, "codex")
	for _, want := range []string{instruction, "Unless the user explicitly selected a pinned delivery thread", "Respect a user-designated current progress thread or explicit thread pin instead"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestWakeupPromptWithoutSavedParentChoosesCurrentThread(t *testing.T) {
	p := BuildPrompt(Task{IssueID: "issue", WakeupID: "wake"}, "codex")
	for _, want := range []string{"latest relevant user message", "omit `--parent`", "new top-level update"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q", want)
		}
	}
}
