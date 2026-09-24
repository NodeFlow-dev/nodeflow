package panel

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func revision(value int64) *int64 { return &value }

func TestRouteUpdateNeedsApplyIncludingFailureRetries(t *testing.T) {
	tests := []struct {
		name    string
		current Route
		desired RouteSpec
		want    bool
	}{
		{
			name:    "disabled draft edit stays local",
			current: Route{Enabled: false, Deployed: false, DeploymentState: "draft"},
			desired: RouteSpec{Enabled: false},
			want:    false,
		},
		{
			name:    "enabled edit applies",
			current: Route{Enabled: true, Deployed: true, DeploymentState: "active"},
			desired: RouteSpec{Enabled: true},
			want:    true,
		},
		{
			name:    "failed enable retries even though actual is off",
			current: Route{Enabled: true, Deployed: false, DeploymentState: "failed"},
			desired: RouteSpec{Enabled: true},
			want:    true,
		},
		{
			name:    "failed disable retries even though desired is off",
			current: Route{Enabled: false, Deployed: true, DeploymentState: "failed"},
			desired: RouteSpec{Enabled: false},
			want:    true,
		},
		{
			name:    "pending delete can be superseded",
			current: Route{Enabled: false, Deployed: false, DeletePending: true, DeploymentState: "deleting"},
			desired: RouteSpec{Enabled: false},
			want:    true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, routeUpdateNeedsApply(test.current, test.desired))
		})
	}
}

func TestRouteFingerprintChangesWithDesiredSpec(t *testing.T) {
	first := RouteSpec{ListenerIP: "*", ListenerPort: 443, SNIs: []string{"a.example"}, TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443, ProxyProtocol: "none", QuotaAction: "observe", Enabled: true}
	second := first
	assert.Equal(t, routeSpecFingerprint(first), routeSpecFingerprint(second))
	second.TargetPort = 8443
	assert.NotEqual(t, routeSpecFingerprint(first), routeSpecFingerprint(second))
	second = first
	second.Enabled = false
	assert.NotEqual(t, routeSpecFingerprint(first), routeSpecFingerprint(second))
	second = first
	second.QuotaPeriod = "daily"
	assert.NotEqual(t, routeSpecFingerprint(first), routeSpecFingerprint(second))
	second = first
	limit := int64(200)
	second.ClientUploadMbps = &limit
	assert.NotEqual(t, routeSpecFingerprint(first), routeSpecFingerprint(second))
	second = first
	second.MatchMode = "destination_ip"
	assert.NotEqual(t, routeSpecFingerprint(first), routeSpecFingerprint(second))
	second = first
	second.HealthCheck = !first.HealthCheck
	assert.NotEqual(t, routeSpecFingerprint(first), routeSpecFingerprint(second))
	second = first
	second.Name = "renamed route"
	assert.NotEqual(t, routeSpecFingerprint(first), routeSpecFingerprint(second))
}

func TestStaleObservedRevisionGuard(t *testing.T) {
	current := int64(8)
	assert.True(t, staleObservedRevision(&current, 7))
	assert.False(t, staleObservedRevision(&current, 8))
	assert.False(t, staleObservedRevision(&current, 9))
	assert.False(t, staleObservedRevision(nil, 1))
}

func TestRouteOptimisticVersionAPI(t *testing.T) {
	f := &fakeStore{}
	body := `{"expected_version":7,"snis":["a.example"],"target_host":"192.0.2.1","target_port":443,"enabled":false}`
	w := request(t, handler(f), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, body, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, f.updatedRoute.ExpectedVersion)
	assert.Equal(t, int64(7), *f.updatedRoute.ExpectedVersion)

	w = request(t, handler(f), http.MethodDelete, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID+"?expected_version=8", "", testAdminToken)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.NotNil(t, f.deleteExpectedVersion)
	assert.Equal(t, int64(8), *f.deleteExpectedVersion)

	w = request(t, handler(f), http.MethodDelete, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID+"?expected_version=0", "", testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	f.routeErr = ErrRouteVersionConflict
	w = request(t, handler(f), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, body, testAdminToken)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"stale_route_version"`)
}

func TestActiveRouteDeleteReturnsPendingState(t *testing.T) {
	route := Route{ID: testRouteID, NodeID: testNodeID, Version: 3, Deployed: true, DeploymentState: "deleting", DeletePending: true, DesiredRevision: revision(4)}
	f := &fakeStore{deleteRouteResult: RouteDeleteResult{Route: route, Pending: true}}
	w := request(t, handler(f), http.MethodDelete, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, "", testAdminToken)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"deployment_state":"deleting"`)
	assert.Contains(t, w.Body.String(), `"delete_pending":true`)
}

func TestRouteDeleteSafetyAndRetry(t *testing.T) {
	assert.True(t, routeCanDeleteImmediately(Route{DeploymentState: "draft"}))
	assert.True(t, routeCanDeleteImmediately(Route{DeploymentState: "disabled", AppliedRevision: revision(4)}))
	assert.False(t, routeCanDeleteImmediately(Route{Enabled: true, Deployed: false, DeploymentState: "failed"}), "failed enable must be superseded before deletion")
	assert.False(t, routeCanDeleteImmediately(Route{Enabled: false, Deployed: true, DeploymentState: "failed"}), "failed active deletion must retry")
	assert.False(t, routeCanDeleteImmediately(Route{DeletePending: true, DeploymentState: "failed"}), "pending delete must remain retryable")
}

func TestRouteActualDecisionHandlesSupersededRevisions(t *testing.T) {
	deleting := Route{DeletePending: true, DesiredRevision: revision(7), DeploymentState: "deleting"}
	decision := routeActualDecisionFor(deleting, 6, false)
	assert.False(t, decision.Delete, "older applied revision cannot finalize a newer delete")
	assert.False(t, decision.ResolveIntent)

	decision = routeActualDecisionFor(deleting, 8, false)
	assert.True(t, decision.Delete, "a later revision that also omits the route finalizes a superseded delete")

	failedEdit := Route{Enabled: true, Deployed: true, DesiredRevision: revision(9), DeploymentState: "failed", DeploymentError: "validation_failed"}
	decision = routeActualDecisionFor(failedEdit, 8, true)
	assert.False(t, decision.ResolveIntent, "old actual config must not erase failure for newer desired spec")

	decision = routeActualDecisionFor(failedEdit, 9, true)
	require.True(t, decision.ResolveIntent)
	assert.Equal(t, "active", decision.State)
	assert.Empty(t, decision.Error)
}

func TestRouteActualDecisionDetectsMembershipMismatch(t *testing.T) {
	route := Route{Enabled: true, DesiredRevision: revision(3), DeploymentState: "pending"}
	decision := routeActualDecisionFor(route, 3, false)
	require.True(t, decision.ResolveIntent)
	assert.Equal(t, "failed", decision.State)
	assert.Equal(t, "observed_route_state_mismatch", decision.Error)
}
