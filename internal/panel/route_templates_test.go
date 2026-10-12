package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type templateTestStore struct {
	*fakeStore
	templates map[string]RouteTemplate
	copies    []Route
	failNode  string
}

func (s *templateTestStore) ListRouteTemplates(context.Context) ([]RouteTemplate, error) {
	out := []RouteTemplate{}
	for _, v := range s.templates {
		out = append(out, v)
	}
	return out, nil
}
func (s *templateTestStore) GetRouteTemplate(_ context.Context, id string) (RouteTemplate, error) {
	v, ok := s.templates[id]
	if !ok {
		return v, ErrNotFound
	}
	return v, nil
}
func (s *templateTestStore) SaveRouteTemplate(_ context.Context, id, name, description string, config json.RawMessage, version int64) (RouteTemplate, error) {
	if id == "" {
		id = testRouteID
	} else if v, ok := s.templates[id]; !ok {
		return RouteTemplate{}, ErrNotFound
	} else if v.Version != version {
		return RouteTemplate{}, ErrRouteVersionConflict
	}
	v := RouteTemplate{ID: id, Name: name, Description: description, Config: config, Version: version + 1}
	s.templates[id] = v
	return v, nil
}
func (s *templateTestStore) DeleteRouteTemplate(_ context.Context, id string, version int64) error {
	v, ok := s.templates[id]
	if !ok {
		return ErrNotFound
	}
	if v.Version != version {
		return ErrRouteVersionConflict
	}
	delete(s.templates, id)
	return nil
}
func (s *templateTestStore) CreateRoute(_ context.Context, nodeID string, spec RouteSpec) (Route, error) {
	if nodeID == s.failNode {
		return Route{}, ErrNotFound
	}
	spec.Enabled = false
	r := routeFromSpec(fmt.Sprintf("%08d-0000-0000-0000-000000000001", len(s.copies)+1), nodeID, spec)
	r.DeploymentState = "draft"
	s.copies = append(s.copies, r)
	return r, nil
}
func templateHandler(s *templateTestStore) http.Handler {
	return NewHandler(s, Config{AdminToken: testAdminToken})
}

func TestPortableRoutePreservesIntent(t *testing.T) {
	payload := `{"name":"portable","match_mode":"sni","snis":["a.example.com","b.example.com"],"listener_ip":"192.0.2.50","listener_port":8443,"proxy_protocol":"v2","accept_proxy_from":["192.0.2.0/24"],"quota_bytes":1000,"quota_action":"observe","quota_period":"daily","client_upload_mbps":10,"client_download_mbps":20,"sticky_mode":"source_table","sticky_ttl":"24h","sticky_table_entries":"200k","sticky_ipv6_prefix":96,"balance_algorithm":"leastconn","slowstart":"5s","custom_fragment":"timeout connect 9s","servers":[{"name":"primary","host":"pool.example.com","port":443,"dns_pool":true,"weight":2,"ip_weights":[{"ip":"192.0.2.10","weight":3,"cost":2}]},{"name":"second","host":"192.0.2.20","port":443,"weight":4}]}`
	spec, err := validatePayload(t, payload)
	require.NoError(t, err)
	route := specToRoute(spec)
	route.Version = 50
	route.NodeID = testNodeID
	route.DeploymentState = "active"
	in, err := portableRouteInput(route)
	require.NoError(t, err)
	raw, err := validatedTemplateConfig(in)
	require.NoError(t, err)
	for _, field := range []string{"node_id", "route_id", "deployment_state", "desired_fingerprint", "created_at", "version"} {
		require.NotContains(t, string(raw), `"`+field+`"`)
	}
	copied, err := validateRoute(in, false)
	require.NoError(t, err)
	spec.Enabled = false
	require.Equal(t, routeSpecFingerprint(spec), routeSpecFingerprint(copied))
	require.False(t, copied.Enabled)
	require.Nil(t, copied.ExpectedVersion)
	// Canonical failover has role flags; clearing balance_mode must not lose them.
	in.Servers[1].Backup = true
	in.StickyMode = ""
	in.BalanceAlgorithm = ""
	in.StickyEnabled = false
	in.StickyTTL = ""
	in.StickyTableEntries = ""
	_, err = validateRoute(in, false)
	require.NoError(t, err)
}

func TestRouteTemplateAPIAndCopy(t *testing.T) {
	spec, err := validatePayload(t, `{"name":"source","match_mode":"sni","snis":["route.example.com"],"target_host":"192.0.2.10","target_port":443}`)
	require.NoError(t, err)
	route := specToRoute(spec)
	route.NodeID = testNodeID
	s := &templateTestStore{fakeStore: &fakeStore{routes: []Route{route}}, templates: map[string]RouteTemplate{}}
	h := templateHandler(s)
	body := fmt.Sprintf(`{"name":"Reusable","source_node_id":%q,"source_route_id":%q}`, testNodeID, testRouteID)
	require.Equal(t, 401, request(t, h, "POST", "/api/v1/route-templates", body, "").Code)
	w := request(t, h, "POST", "/api/v1/route-templates", body, testAdminToken)
	require.Equal(t, 201, w.Code, w.Body.String())
	var template RouteTemplate
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &template))
	require.Equal(t, int64(1), template.Version)
	require.Equal(t, 200, request(t, h, "GET", "/api/v1/route-templates", "", testAdminToken).Code)
	require.Equal(t, 200, request(t, h, "GET", "/api/v1/route-templates/"+template.ID, "", testAdminToken).Code)
	other := "22222222-2222-2222-2222-222222222222"
	s.failNode = other
	path := "/api/v1/route-templates/" + template.ID + "/apply"
	apply := fmt.Sprintf(`{"node_ids":[%q,%q],"expected_version":1}`, testNodeID, other)
	w = request(t, h, "POST", path, apply, testAdminToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	var results []routeCopyResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &results))
	require.Len(t, results, 2)
	require.NotNil(t, results[0].Route)
	require.False(t, results[0].Route.Enabled)
	require.Equal(t, "draft", results[0].Route.DeploymentState)
	require.NotEmpty(t, results[1].Error)
	require.Nil(t, results[1].Route)
	require.Len(t, s.copies, 1)
	require.Equal(t, "source", s.copies[0].Name)
	require.Equal(t, 409, request(t, h, "POST", path, fmt.Sprintf(`{"node_ids":[%q],"expected_version":2}`, testNodeID), testAdminToken).Code)
	require.Equal(t, 400, request(t, h, "POST", path, fmt.Sprintf(`{"node_ids":[%q,%q],"expected_version":1}`, testNodeID, testNodeID), testAdminToken).Code)
	update := fmt.Sprintf(`{"name":"Updated","description":"Reusable settings\nSecond line","expected_version":1,"config":%s}`, template.Config)
	w = request(t, h, "PUT", "/api/v1/route-templates/"+template.ID, update, testAdminToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, 409, request(t, h, "PUT", "/api/v1/route-templates/"+template.ID, update, testAdminToken).Code)
	require.Equal(t, 409, request(t, h, "DELETE", "/api/v1/route-templates/"+template.ID, `{"expected_version":1}`, testAdminToken).Code)
	require.Equal(t, 204, request(t, h, "DELETE", "/api/v1/route-templates/"+template.ID, `{"expected_version":2}`, testAdminToken).Code)
	require.Equal(t, 404, request(t, h, "GET", "/api/v1/route-templates/"+template.ID, "", testAdminToken).Code)
	// Template edits/deletion cannot alter already-created drafts or the source.
	require.Equal(t, "source", s.copies[0].Name)
	require.True(t, route.Enabled)
	copyPath := "/api/v1/nodes/" + testNodeID + "/routes/" + testRouteID + "/copy"
	w = request(t, h, "POST", copyPath, fmt.Sprintf(`{"node_ids":[%q],"name":"Copy"}`, testNodeID), testAdminToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Len(t, s.copies, 2)
	require.Equal(t, "Copy", s.copies[1].Name)
	require.NotEqual(t, s.copies[0].ID, s.copies[1].ID)
}

func TestTemplateNormalizesDefaultsAndEnabled(t *testing.T) {
	in := decodeRouteInput(t, `{"name":"minimal","hostname":"route.example.com","target_host":"192.0.2.10","target_port":443,"enabled":true,"expected_version":99}`)
	raw, err := validatedTemplateConfig(in)
	require.NoError(t, err)
	var normalized routeInput
	require.NoError(t, json.Unmarshal(raw, &normalized))
	require.Equal(t, "sni", normalized.MatchMode)
	require.NotNil(t, normalized.ListenerPort)
	require.Equal(t, 443, *normalized.ListenerPort)
	require.Equal(t, "*", normalized.ListenerIP)
	require.Equal(t, "none", normalized.ProxyProtocol)
	require.False(t, *normalized.Enabled)
	require.Nil(t, normalized.ExpectedVersion)
	original, err := validateRoute(in, false)
	require.NoError(t, err)
	original.Enabled = false
	original.ExpectedVersion = nil
	saved, err := validateRoute(normalized, false)
	require.NoError(t, err)
	require.Equal(t, routeSpecFingerprint(original), routeSpecFingerprint(saved))
}
