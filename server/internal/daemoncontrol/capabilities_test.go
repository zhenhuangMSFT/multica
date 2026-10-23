package daemoncontrol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/util"
	lifecyclev1 "github.com/multica-ai/multica/server/pkg/lifecycle/v1"
)

func TestMaintenanceCapabilitiesWithoutExtension(t *testing.T) {
	for _, tc := range []struct {
		name, token, runtime string
		directory            *fixtureDirectory
		store                Store
		status               int
		backend              string
	}{
		{"fenced-core", userToken(t, fixtureOwner), fixtureRuntime, directoryFixture(), NewFencedStore(&fixtureStore{}, &admissionSpy{}), 200, lifecyclev1.CoverageSupported},
		{"unfenced-store", userToken(t, fixtureOwner), fixtureRuntime, directoryFixture(), &fixtureStore{}, 200, lifecyclev1.CoverageUnsupported},
		{"missing-store", userToken(t, fixtureOwner), fixtureRuntime, directoryFixture(), nil, 503, ""},
		{"missing-auth", "", fixtureRuntime, directoryFixture(), &fixtureStore{}, 401, ""},
		{"task-owner", "mat_fixture", fixtureRuntime, directoryFixture(), &fixtureStore{}, 403, ""},
		{"plugin-token", "mpi_fixture", fixtureRuntime, directoryFixture(), &fixtureStore{}, 401, ""},
		{"admin-not-owner", userToken(t, fixtureOther), fixtureRuntime, directoryFixture(), &fixtureStore{}, 403, ""},
		{"malformed-runtime", userToken(t, fixtureOwner), "bad", directoryFixture(), &fixtureStore{}, 400, ""},
		{"directory-failure", userToken(t, fixtureOwner), fixtureRuntime, &fixtureDirectory{rows: directoryFixture().rows, queryErr: errors.New("fixture unavailable")}, &fixtureStore{}, 503, ""},
		{"membership-failure", userToken(t, fixtureOwner), fixtureRuntime, &fixtureDirectory{rows: directoryFixture().rows, memberErr: errors.New("fixture unavailable")}, &fixtureStore{}, 503, ""},
		{"mixed-owner", userToken(t, fixtureOwner), fixtureRuntime, mixedOwnerDirectory(), &fixtureStore{}, 403, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/runtimes/"+tc.runtime+"/maintenance/capabilities", nil)
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response := httptest.NewRecorder()
			controlRoutes(NewControl(tc.directory, tc.store)).ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("HTTP %d, want %d: %s", response.Code, tc.status, response.Body)
			}
			if tc.status != http.StatusOK {
				return
			}
			var got lifecyclev1.MaintenanceCapabilities
			if err := lifecyclev1.DecodeMaintenanceJSON(response.Body, &got); err != nil || got.Validate() != nil {
				t.Fatalf("invalid capability response: %+v %v", got, err)
			}
			if got.BackendAdmission != tc.backend || got.DaemonAdmission != lifecyclev1.CoverageUnknown ||
				got.SupervisorLaunch != lifecyclev1.CoverageUnsupported || got.CompleteDrain ||
				got.DaemonID != "fixture-daemon" || len(got.Scope) != 64 {
				t.Fatalf("unsupported external coverage asserted: %+v", got)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("capability binding may be cached")
			}
		})
	}
}

func TestMaintenanceCapabilitiesClientToHandler(t *testing.T) {
	server := httptest.NewServer(controlRoutes(NewControl(directoryFixture(), NewFencedStore(&fixtureStore{}, &admissionSpy{}))))
	defer server.Close()
	client := cli.NewAPIClient(server.URL, fixtureWorkspace, userToken(t, fixtureOwner))
	got, err := client.MaintenanceCapabilities(context.Background(), fixtureRuntime)
	if err != nil || got == nil || got.Contract != lifecyclev1.Contract ||
		got.BackendAdmission != lifecyclev1.CoverageSupported || got.CompleteDrain {
		t.Fatalf("capability roundtrip: %+v %v", got, err)
	}
}

func mixedOwnerDirectory() *fixtureDirectory {
	directory := directoryFixture()
	row := directory.rows[0]
	row.ID, row.OwnerID = util.MustParseUUID(fixtureOther), util.MustParseUUID(fixtureOther)
	directory.rows = append(directory.rows, row)
	return directory
}
