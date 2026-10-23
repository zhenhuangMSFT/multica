package plugin_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

func TestRuntimeManagerPreviewBundle(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate standalone plugin source")
	}
	root := filepath.Dir(file)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, entry := range []string{plugincontract.ManifestFilename, "ui/main.js", "ui/preview.html", "fixtures/missing-manager.json", "ui/main.test.cjs"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry)))
		if err != nil {
			t.Fatal(err)
		}
		file, err := writer.Create(entry)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	bundle, err := plugincontract.ParseBundle(archive.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	manifest := bundle.Manifest
	if manifest.ManifestVersion != 1 || manifest.Version != "0.1.0-dev" ||
		manifest.Key != "ai.multica.runtime-manager-preview" || len(manifest.Scopes) != 1 || manifest.Scopes[0] != plugincontract.ScopeStorageUser ||
		len(manifest.Contributes.Hooks) != 0 || len(manifest.Contributes.Resources) != 0 ||
		len(manifest.Contributes.Surfaces) != 1 || len(bundle.Files) != 1 {
		t.Fatalf("preview added authority or executable contributions: %+v", manifest)
	}
	surface := manifest.Contributes.Surfaces[0]
	if surface.Type != plugincontract.SurfaceSidebarPanel || surface.Entry != "ui/main.js" ||
		len(surface.Platforms) != 2 || surface.Platforms[0] != "web" || surface.Platforms[1] != "desktop" {
		t.Fatalf("existing shared surface contract changed: %+v", surface)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(bundle.Canonical, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"install", "uninstall", "executor", "endpoint", "credentials"} {
		if _, exists := raw[key]; exists {
			t.Fatalf("plugin artifact unexpectedly declares %s", key)
		}
	}
	if _, ok := bundle.File("ui/main.js"); !ok {
		t.Fatal("single classic-script artifact missing")
	}
	for _, entry := range []string{"ui/preview.html", "fixtures/missing-manager.json", "ui/main.test.cjs"} {
		if _, ok := bundle.File(entry); ok {
			t.Fatalf("unreferenced fixture was retained in the plugin artifact: %s", entry)
		}
	}
}
