package panel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedWebHandlerServesReactProductionBundle(t *testing.T) {
	handler := embeddedWebHandler()

	request := httptest.NewRequest(http.MethodGet, "/nodes/example", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("SPA route status = %d, want 200", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, `id="root"`) {
		t.Fatal("embedded index is not the React application shell")
	}
	if strings.Contains(body, `src="/app.js"`) {
		t.Fatal("legacy frontend is embedded instead of the React production bundle")
	}

	asset := regexp.MustCompile(`src="(/[^"]+\.js)"`).FindStringSubmatch(body)
	if len(asset) != 2 {
		t.Fatal("React application shell does not reference a production JavaScript asset")
	}
	assetRequest := httptest.NewRequest(http.MethodGet, asset[1], nil)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK {
		payload, _ := io.ReadAll(assetResponse.Result().Body)
		t.Fatalf("embedded JavaScript asset status = %d, want 200: %s", assetResponse.Code, payload)
	}

	missingRequest := httptest.NewRequest(http.MethodGet, "/assets/does-not-exist.js", nil)
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missingRequest)
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", missingResponse.Code)
	}
}
