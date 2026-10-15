package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type advancedConfigTemplateStore struct{ *templateTestStore }

func (s *advancedConfigTemplateStore) CreateRoute(ctx context.Context, nodeID string, spec RouteSpec) (Route, error) {
	if nodeID == s.failNode {
		return Route{}, fmt.Errorf("create route: %w", ErrAdvancedConfig)
	}
	return s.templateTestStore.CreateRoute(ctx, nodeID, spec)
}

func TestRouteCopyReviewFixes(t *testing.T) {
	spec, err := validatePayload(t, `{"name":"source","match_mode":"sni","snis":["route.example.com"],"target_host":"192.0.2.10","target_port":443}`)
	require.NoError(t, err)
	route := specToRoute(spec)
	route.NodeID = testNodeID
	other := "22222222-2222-2222-2222-222222222222"
	manual := "33333333-3333-4333-8333-333333333333"
	base := &templateTestStore{fakeStore: &fakeStore{routes: []Route{route}}, templates: map[string]RouteTemplate{}, failNode: manual}
	s := &advancedConfigTemplateStore{base}
	h := NewHandler(s, Config{AdminToken: testAdminToken})

	// Copy without a name: same node gets the suffix, other nodes keep the name;
	// a node in manual HAProxy mode reports the advanced-config message.
	copyPath := "/api/v1/nodes/" + testNodeID + "/routes/" + testRouteID + "/copy"
	w := request(t, h, "POST", copyPath, fmt.Sprintf(`{"node_ids":[%q,%q,%q]}`, testNodeID, other, manual), testAdminToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	var results []routeCopyResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &results))
	require.Len(t, results, 3)
	require.Equal(t, "source (копия)", results[0].Route.Name)
	require.Equal(t, "source", results[1].Route.Name)
	require.Equal(t, ErrAdvancedConfig.Error(), results[2].Error)
	require.Equal(t, "advanced_config_active", results[2].ErrorCode)
	event := base.auditEvents[len(base.auditEvents)-1]
	require.Equal(t, "route.copy", event.Action)
	require.Equal(t, "route", event.ResourceType)
	require.Equal(t, testRouteID, event.ResourceID)
	require.Equal(t, []string{testNodeID, other, manual}, event.Details["node_ids"])

	// The UI may localize the same-node suffix.
	w = request(t, h, "POST", copyPath, fmt.Sprintf(`{"node_ids":[%q],"copy_suffix":" (copy)"}`, testNodeID), testAdminToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	results = nil
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &results))
	require.Equal(t, "source (copy)", results[0].Route.Name)

	// Template lifecycle audit and schema_version.
	w = request(t, h, "POST", "/api/v1/route-templates", fmt.Sprintf(`{"name":"T","source_node_id":%q,"source_route_id":%q}`, testNodeID, testRouteID), testAdminToken)
	require.Equal(t, 201, w.Code, w.Body.String())
	var template RouteTemplate
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &template))
	require.Contains(t, string(template.Config), `"schema_version":1`)
	event = base.auditEvents[len(base.auditEvents)-1]
	require.Equal(t, "route_template.create", event.Action)
	require.Equal(t, "route_template", event.ResourceType)
	require.Equal(t, template.ID, event.ResourceID)

	// GET output can be sent back unchanged; a newer schema is refused.
	update := fmt.Sprintf(`{"name":"T2","expected_version":1,"config":%s}`, template.Config)
	require.Equal(t, 200, request(t, h, "PUT", "/api/v1/route-templates/"+template.ID, update, testAdminToken).Code)
	require.Equal(t, "route_template.update", base.auditEvents[len(base.auditEvents)-1].Action)
	future := `{"name":"T3","expected_version":2,"config":{"schema_version":2,"name":"x","hostname":"a.example.com","target_host":"192.0.2.1","target_port":443}}`
	w = request(t, h, "PUT", "/api/v1/route-templates/"+template.ID, future, testAdminToken)
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), "schema_version 2")

	stored := base.templates[template.ID]
	stored.Config = json.RawMessage(`{"schema_version":2,"name":"x"}`)
	base.templates[template.ID] = stored
	w = request(t, h, "POST", "/api/v1/route-templates/"+template.ID+"/apply", fmt.Sprintf(`{"node_ids":[%q],"expected_version":2}`, other), testAdminToken)
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), "unsupported_template_schema")

	// A config without schema_version (stored by the first PR build) is schema 1.
	stored.Config = json.RawMessage(`{"name":"legacy","hostname":"a.example.com","target_host":"192.0.2.1","target_port":443}`)
	base.templates[template.ID] = stored
	w = request(t, h, "POST", "/api/v1/route-templates/"+template.ID+"/apply", fmt.Sprintf(`{"node_ids":[%q],"expected_version":2}`, other), testAdminToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	event = base.auditEvents[len(base.auditEvents)-1]
	require.Equal(t, "route_template.apply", event.Action)
	require.Equal(t, template.ID, event.ResourceID)
	require.Equal(t, []string{other}, event.Details["node_ids"])

	require.Equal(t, 204, request(t, h, "DELETE", "/api/v1/route-templates/"+template.ID, `{"expected_version":2}`, testAdminToken).Code)
	event = base.auditEvents[len(base.auditEvents)-1]
	require.Equal(t, "route_template.delete", event.Action)
	require.Equal(t, template.ID, event.ResourceID)
}
