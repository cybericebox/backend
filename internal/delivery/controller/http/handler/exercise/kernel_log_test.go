package exercise_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

// An editor save keeps the flag, and a device sent without it has it off (the past bug: a save that erased what the DTO did not
// carry).
func TestAnEditorSaveKeepsTheKernelLog(t *testing.T) {
	uid, exID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	uc := &fakeUC{versionView: exerciseUseCase.VersionView{ID: uuid.Must(uuid.NewV7()), ExerciseID: exID}}
	r := newEngine(uc, uid)
	put := func(device string) exerciseModel.Device {
		t.Helper()
		body := `{"Variants":[{"Index":1,"Tasks":[],"Topology":{"VPN":{},"Internet":{},"Devices":[` + device + `]}}]}`
		req := httptest.NewRequest(http.MethodPut, "/api/exercises/"+exID.String()+"/draft", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		return uc.savedVariants[0].Topology.Devices[0]
	}
	id := uuid.Must(uuid.NewV7()).String()
	saved := put(`{"ID":"` + id + `","Name":"r1","Type":"node","Profile":"microvm","ConsolePorts":[{"Name":"console0"}],"KernelLog":true}`)
	if !saved.KernelLog || len(saved.ConsolePorts) != 1 {
		t.Fatalf("saved: %+v", saved)
	}
	if put(`{"ID":"` + id + `","Name":"r1","Type":"node","Profile":"microvm","ConsolePorts":[{"Name":"console0"}]}`).KernelLog {
		t.Fatal("a device sent without the flag has it off")
	}
}

func TestEditorCapabilitiesTellTheKernelLog(t *testing.T) {
	uc := &fakeUC{kernelLog: true}
	w := httptest.NewRecorder()
	newEngine(uc, uuid.Must(uuid.NewV7())).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/exercises/capabilities", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"KernelLogAvailable":true`) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}
