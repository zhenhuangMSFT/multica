package agent

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCopilotExecutePassesManagedMcpConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		resume string
		config string
	}{
		{"fresh", "", `{"mcpServers":{"DevBox":{"command":"npx.cmd","args":["-y","example-test-package"],"env":{"TEST_SECRET":"not-a-real-secret"}}}}`},
		{"resumed", "prior-test-session", `{"mcpServers":{"DevBox":{"command":"npx.cmd","args":["-y","example-test-package"]}}}`},
		{"empty-managed", "", `{"mcpServers":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testCopilotManagedMcpExecution(t, tc.resume, json.RawMessage(tc.config))
		})
	}
}

func testCopilotManagedMcpExecution(t *testing.T, resume string, raw json.RawMessage) {
	t.Helper()
	dir := t.TempDir()
	fakePath := filepath.Join(dir, "copilot")
	if runtime.GOOS == "windows" {
		fakePath += ".cmd"
		if err := os.WriteFile(fakePath, []byte("@echo off\r\nexit /b 99\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		script := `$ErrorActionPreference = 'Stop'
$null = [Console]::In.ReadToEnd()
$index = [Array]::IndexOf($args, '--additional-mcp-config')
if ($index -lt 0) { exit 7 }
$path = $args[$index + 1].Substring(1)
$payload = @{ path = $path; config = (Get-Content -LiteralPath $path -Raw | ConvertFrom-Json) } | ConvertTo-Json -Depth 10 -Compress
@{ type = 'assistant.message'; data = @{ content = $payload } } | ConvertTo-Json -Depth 12 -Compress
'{"type":"result","sessionId":"fake-mcp-session","exitCode":0}'
`
		if err := os.WriteFile(strings.TrimSuffix(fakePath, ".cmd")+".ps1", []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		script := `#!/bin/sh
cat >/dev/null
while [ "$#" -gt 0 ] && [ "$1" != "--additional-mcp-config" ]; do shift; done
[ "$#" -gt 1 ] || exit 7
shift
path="${1#@}"
printf '{"type":"assistant.message","data":{"content":'
printf '"%s"}}\n' "$path"
printf '%s\n' '{"type":"result","sessionId":"fake-mcp-session","exitCode":0}'
`
		if err := os.WriteFile(fakePath, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	backend, err := New("copilot", Config{ExecutablePath: fakePath, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(t.Context(), "test prompt", ExecOptions{
		McpConfig: raw, Timeout: 15 * time.Second, ResumeSessionID: resume,
	})
	if err != nil {
		t.Fatal(err)
	}

	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("managed MCP did not reach the child: status=%s error=%s", result.Status, result.Error)
	}
	var path string
	if runtime.GOOS == "windows" {
		var payload struct {
			Path   string          `json:"path"`
			Config json.RawMessage `json:"config"`
		}
		if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err := json.Unmarshal(payload.Config, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Fatal("child did not receive the configured server")
		}
		path = payload.Path
	} else {
		path = result.Output
	}
	// Messages closes after the execution goroutine's cleanup has finished.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary MCP config was not removed: %v", err)
	}
}

func TestCopilotManagedMcpConfigCleanupOnStartFailure(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMP", tempRoot)
	t.Setenv("TMPDIR", tempRoot)
	fakePath := filepath.Join(t.TempDir(), "copilot")
	if runtime.GOOS == "windows" {
		fakePath += ".exe"
	}
	if err := os.WriteFile(fakePath, []byte("not an executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := New("copilot", Config{ExecutablePath: fakePath, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Execute(t.Context(), "test", ExecOptions{
		McpConfig: json.RawMessage(`{"mcpServers":{"DevBox":{"command":"test-only"}}}`),
	})
	if err == nil || !strings.Contains(err.Error(), "start copilot") {
		t.Fatalf("expected startup error, got %v", err)
	}
	files, err := filepath.Glob(filepath.Join(tempRoot, "multica-mcp-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary MCP configuration leaked after startup failure: %v %v", files, err)
	}
}

func TestCopilotManagedMcpConfigWriteFailureDoesNotStartChild(t *testing.T) {
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMP", blockedRoot)
	t.Setenv("TMPDIR", blockedRoot)
	fakePath := filepath.Join(t.TempDir(), "copilot")
	if runtime.GOOS == "windows" {
		fakePath += ".exe"
	}
	if err := os.WriteFile(fakePath, []byte("must never start"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := New("copilot", Config{ExecutablePath: fakePath, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Execute(t.Context(), "test", ExecOptions{McpConfig: json.RawMessage(`{"mcpServers":{}}`)})
	if err == nil || !strings.Contains(err.Error(), "create mcp config temp dir") {
		t.Fatalf("expected MCP preparation error before child startup, got %v", err)
	}
}
