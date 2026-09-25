package panel

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A 509-512 byte expert directive was accepted on save, stored with its
// four-space indent (516 bytes) and then rejected by the renderer's
// re-validation of stored intent, failing the whole node config with
// invalid_route_set.
func TestCustomFragmentMaxLineSurvivesStoreAndRender(t *testing.T) {
	directive := strings.Repeat("a", 512)
	for _, fragment := range []string{directive, "    " + directive, "\t" + directive} {
		payload, err := json.Marshal(map[string]any{
			"name": "expert", "listener_ip": "*", "listener_port": 443, "snis": []string{"app.example.com"},
			"target_type": "tcp", "target_host": "192.0.2.10", "target_port": 443, "custom_fragment": fragment,
		})
		require.NoError(t, err)
		spec, err := validateRoute(decodeRouteInput(t, string(payload)), false)
		require.NoError(t, err)
		assert.Equal(t, "    "+directive, spec.CustomFragment)
		got, err := RenderHAProxyConfig([]Route{specToRoute(spec)})
		require.NoError(t, err)
		assert.Contains(t, got.Config, "\n    "+directive+"\n")
	}
	_, err := normalizeCustomFragment(strings.Repeat("a", 513))
	require.EqualError(t, err, "custom_fragment lines must not exceed 512 bytes")
}

// nodes.name has CHECK (length(name) BETWEEN 1 AND 200). A longer name from
// the node settings dialog reached the database and surfaced as a 500.
func TestNodeNameLengthIsValidatedBeforeTheStore(t *testing.T) {
	f := &fakeStore{}
	long := strings.Repeat("я", 201)
	w := request(t, handler(f), "POST", "/api/v1/nodes", `{"name":"`+long+`","address":"192.0.2.10"}`, testAdminToken)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "name must not exceed 200 characters")
	assert.Empty(t, f.createdNode.Name)

	w = request(t, handler(f), "PUT", "/api/v1/nodes/11111111-1111-4111-8111-111111111111", `{"name":"`+long+`","address":"192.0.2.10"}`, testAdminToken)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "name must not exceed 200 characters")

	w = request(t, handler(f), "POST", "/api/v1/nodes", `{"name":"`+strings.Repeat("я", 200)+`","address":"192.0.2.10"}`, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

