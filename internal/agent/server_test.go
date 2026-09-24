package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls        [][]string
	failReload   bool
	failValidate bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == "systemctl" && f.failReload {
		return []byte("reload failed"), errors.New("exit 1")
	}
	if name == "haproxy" && f.failValidate {
		return []byte("secret filesystem detail"), errors.New("exit 1")
	}
	return []byte("HAProxy version 3.0"), nil
}

func testServer(t *testing.T, runner *fakeRunner) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	cfg := Config{Token: "secret", ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	manager := ConfigManager{Runner: runner, ManagedConfig: path, HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	return NewServer(cfg, &manager, "test"), path
}

func request(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestServerRequiresBearerToken(t *testing.T) {
	s, _ := testServer(t, &fakeRunner{})
	rr := request(s.Handler(), http.MethodGet, "/v1/info", "", "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestServerValidate(t *testing.T) {
	runner := &fakeRunner{}
	s, _ := testServer(t, runner)
	rr := request(s.Handler(), http.MethodPost, "/v1/config/validate", "secret", `{"config":"global\n  daemon\n"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	if len(runner.calls) != 1 || runner.calls[0][1] != "-c" {
		t.Fatalf("calls=%v", runner.calls)
	}
}

func TestServerApply(t *testing.T) {
	runner := &fakeRunner{}
	s, path := testServer(t, runner)
	rr := request(s.Handler(), http.MethodPost, "/v1/config/apply", "secret", `{"config":"global\n  daemon\n"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	if len(runner.calls) != 2 || !slices.Equal(runner.calls[1], []string{"systemctl", "reload-or-restart", "haproxy.service"}) {
		t.Fatalf("calls=%v", runner.calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestServerApplyRevisionIdempotentAndRollback(t *testing.T) {
	runner := &fakeRunner{}
	s, _ := testServer(t, runner)
	first := request(s.Handler(), http.MethodPost, "/v1/config/apply", "secret", `{"config":"one","revision":"rev-1"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first=%d %s", first.Code, first.Body)
	}
	calls := len(runner.calls)
	second := request(s.Handler(), http.MethodPost, "/v1/config/apply", "secret", `{"config":"one","revision":"rev-1"}`)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"idempotent":true`) {
		t.Fatalf("second=%d %s", second.Code, second.Body)
	}
	if len(runner.calls) != calls {
		t.Fatal("idempotent request executed commands")
	}
	third := request(s.Handler(), http.MethodPost, "/v1/config/apply", "secret", `{"config":"two","revision":"rev-2"}`)
	if third.Code != http.StatusOK {
		t.Fatalf("third=%d %s", third.Code, third.Body)
	}
	rollback := request(s.Handler(), http.MethodPost, "/v1/config/rollback", "secret", "")
	if rollback.Code != http.StatusOK || !strings.Contains(rollback.Body.String(), `"actual_revision":"rev-1"`) {
		t.Fatalf("rollback=%d %s", rollback.Code, rollback.Body)
	}
}

func TestConfigErrorsAreSanitized(t *testing.T) {
	s, _ := testServer(t, &fakeRunner{failValidate: true})
	rr := request(s.Handler(), http.MethodPost, "/v1/config/validate", "secret", `{"config":"bad","revision":"rev-1"}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), "secret filesystem detail") {
		t.Fatalf("unsanitized response: %s", rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "validation_failed") {
		t.Fatalf("body=%s", rr.Body)
	}
}

func TestServerRejectsOversizedConfig(t *testing.T) {
	s, _ := testServer(t, &fakeRunner{})
	body := `{"config":"` + strings.Repeat("x", MaxManagedConfigBytes+1) + `"}`
	rr := request(s.Handler(), http.MethodPost, "/v1/config/apply", "secret", body)
	if rr.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rr.Body.String(), "config_too_large") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
}

func TestServerUpdateVerifyOffAndAuthentication(t *testing.T) {
	s, _ := testServer(t, &fakeRunner{})
	updater, err := NewUpdateVerifier(UpdateVerifierConfig{Mode: UpdateModeOff})
	if err != nil {
		t.Fatal(err)
	}
	s.Updater = updater
	body, _ := json.Marshal(UpdateManifest{ArtifactPath: "/ignored-while-off"})

	unauthorized := request(s.Handler(), http.MethodPost, "/v1/update/verify", "", string(body))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body)
	}
	verified := request(s.Handler(), http.MethodPost, "/v1/update/verify", "secret", string(body))
	if verified.Code != http.StatusOK || !strings.Contains(verified.Body.String(), `"status":"off"`) {
		t.Fatalf("verify off=%d %s", verified.Code, verified.Body)
	}
}

func TestServerUpdateVerifySignedArtifact(t *testing.T) {
	fixture := newUpdateFixture(t)
	s, _ := testServer(t, &fakeRunner{})
	s.Updater = fixture.verifier(t, 0)
	body, err := json.Marshal(fixture.manifest)
	if err != nil {
		t.Fatal(err)
	}

	response := request(s.Handler(), http.MethodPost, "/v1/update/verify", "secret", string(body))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"verified"`) {
		t.Fatalf("verify=%d %s", response.Code, response.Body)
	}
	trailing := request(s.Handler(), http.MethodPost, "/v1/update/verify", "secret", string(body)+` {}`)
	if trailing.Code != http.StatusBadRequest {
		t.Fatalf("trailing=%d %s", trailing.Code, trailing.Body)
	}
}
