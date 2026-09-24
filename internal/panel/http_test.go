package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nodeflow/nodeflow/internal/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

const testAdminToken = "0123456789abcdef0123456789abcdef"
const testNodeID = "11111111-1111-4111-8111-111111111111"
const testRouteID = "22222222-2222-4222-8222-222222222222"

type fakeStore struct {
	renderFacts           NodeRenderFacts
	healthErr             error
	panelSettings         PanelSettings
	panelSettingsErr      error
	updatedPanelSettings  PanelSettings
	nodes                 []Node
	dashboardOverview     DashboardOverview
	dashboardOverviewErr  error
	dashboardNodeID       string
	dashboardRange        string
	routes                []Route
	createdNode           nodeInput
	createdRouteFor       string
	createdRoute          RouteSpec
	updatedRouteFor       string
	updatedRouteID        string
	updatedRoute          RouteSpec
	deletedRouteFor       string
	deletedRouteID        string
	deleteRouteResult     RouteDeleteResult
	deleteExpectedVersion *int64
	routeErr              error
	agentVersion          string
	enrollmentHash        string
	enrollmentFor         string
	heartbeatToken        string
	heartbeat             Heartbeat
	heartbeatResult       HeartbeatResult
	heartbeatErr          error
	createdConfig         configRevisionInput
	desiredRevision       int64
	applyToken            string
	applyReport           ApplyReport
	applyErr              error
	operational           NodeOperationalDetail
	traffic               NodeTrafficReport
	trafficMonth          time.Time
	trafficErr            error
	trafficHistory        TrafficHistory
	trafficHistoryRange   string
	trafficHistoryErr     error
	routeTrafficHistory   TrafficHistory
	routeHistoryNodeID    string
	routeHistoryRouteID   string
	routeHistoryRange     string
	routeHistoryErr       error
	firewallPolicy        NodeFirewallPolicy
	releases              []AgentRelease
	nextReleaseSequence   int64
	deleteReleaseErr      error
	updateState           NodeAgentUpdateState
	assignReleaseErr      error
	assignedReleaseID     string
	assignExpectedActual  int64
	assignExpectedDesired int64
	restartNodeID         string
	restartGeneration     int64
	restartErr            error
	auditEvents           []AuditEvent
	auditEntries          []AuditEntry
	revokedTokenID        string
	revokedExceptID       string
	revokeTokenCalls      int
	revokeOtherCalls      int
	revokeOtherFails      int
	credentialUsed        *bool
	credentialUseErr      error
	renewalToken          string
	renewalIdentity       AgentCredentialIdentity
	renewalRequest        CredentialRenewalRequest
	renewalCandidate      CredentialRenewalCandidate
	renewalRecord         CredentialRenewalRecord
	renewalExisting       bool
	renewalErr            error
	confirmedRenewalID    string
	confirmToken          string
	confirmIdentity       AgentCredentialIdentity
	confirmErr            error
	getNodeHook           func(context.Context, string) (Node, error)
	getFirewallPolicyHook func(context.Context, string) (NodeFirewallPolicy, error)
	appendAuditHook       func(context.Context, AuditEvent) error
}

func (f *fakeStore) Health(context.Context) error { return f.healthErr }
func (f *fakeStore) GetPanelSettings(context.Context) (PanelSettings, error) {
	if f.panelSettingsErr != nil {
		return PanelSettings{}, f.panelSettingsErr
	}
	if validatePanelSettings(f.panelSettings) != nil {
		f.panelSettings = defaultPanelSettings()
	}
	return f.panelSettings, nil
}
func (f *fakeStore) UpdatePanelSettings(_ context.Context, settings PanelSettings) (PanelSettings, error) {
	if f.panelSettingsErr != nil {
		return PanelSettings{}, f.panelSettingsErr
	}
	settings.UpdatedAt = time.Now().UTC()
	f.updatedPanelSettings = settings
	f.panelSettings = settings
	return settings, nil
}
func (f *fakeStore) ListNodes(context.Context) ([]Node, error)                  { return f.nodes, nil }
func (f *fakeStore) ReorderNodes(_ context.Context, _ []string) ([]Node, error) { return f.nodes, nil }
func (f *fakeStore) GetDashboardOverview(_ context.Context, nodeID, rangeValue string) (DashboardOverview, error) {
	f.dashboardNodeID = nodeID
	f.dashboardRange = rangeValue
	return f.dashboardOverview, f.dashboardOverviewErr
}
func (f *fakeStore) CreateNode(_ context.Context, name, address string, metadata map[string]any) (Node, error) {
	f.createdNode = nodeInput{Name: name, Address: address, Metadata: metadata}
	return Node{ID: testNodeID, Name: name, Address: address, Metadata: metadata}, nil
}
func (f *fakeStore) GetNode(ctx context.Context, id string) (Node, error) {
	if f.getNodeHook != nil {
		return f.getNodeHook(ctx, id)
	}
	if len(f.nodes) > 0 {
		return f.nodes[0], nil
	}
	return Node{ID: id, Name: "edge-1", Address: "192.0.2.10", Metadata: map[string]any{}}, nil
}
func (f *fakeStore) GetNodeOperationalDetail(context.Context, string) (NodeOperationalDetail, error) {
	return f.operational, nil
}
func (f *fakeStore) GetTraffic(_ context.Context, nodeID string, month time.Time) (NodeTrafficReport, error) {
	f.trafficMonth = month
	if f.traffic.NodeID == "" {
		f.traffic.NodeID = nodeID
		f.traffic.Month = month.Format("2006-01")
	}
	return f.traffic, f.trafficErr
}
func (f *fakeStore) GetTrafficHistory(_ context.Context, _ string, rangeValue string) (TrafficHistory, error) {
	f.trafficHistoryRange = rangeValue
	return f.trafficHistory, f.trafficHistoryErr
}
func (f *fakeStore) GetRouteTrafficHistory(_ context.Context, nodeID, routeID, rangeValue string) (TrafficHistory, error) {
	f.routeHistoryNodeID = nodeID
	f.routeHistoryRouteID = routeID
	f.routeHistoryRange = rangeValue
	return f.routeTrafficHistory, f.routeHistoryErr
}
func (f *fakeStore) GetFirewallPolicy(ctx context.Context, nodeID string) (NodeFirewallPolicy, error) {
	if f.getFirewallPolicyHook != nil {
		return f.getFirewallPolicyHook(ctx, nodeID)
	}
	if f.firewallPolicy.NodeID == "" {
		f.firewallPolicy = NodeFirewallPolicy{NodeID: nodeID, Mode: "observe"}
	}
	return f.firewallPolicy, nil
}
func (f *fakeStore) UpdateFirewallPolicy(_ context.Context, nodeID, mode string) (NodeFirewallPolicy, error) {
	f.firewallPolicy = NodeFirewallPolicy{NodeID: nodeID, Mode: mode}
	return f.firewallPolicy, nil
}
func (f *fakeStore) ReserveAgentReleaseSequence(context.Context) (int64, error) {
	for _, release := range f.releases {
		if release.Sequence > f.nextReleaseSequence {
			f.nextReleaseSequence = release.Sequence
		}
	}
	f.nextReleaseSequence++
	return f.nextReleaseSequence, nil
}
func (f *fakeStore) CreateAgentRelease(_ context.Context, release AgentRelease) (AgentRelease, error) {
	if release.ID == "" {
		release.ID = fmt.Sprintf("22222222-2222-4222-8222-%012d", len(f.releases)+1)
	}
	f.releases = append(f.releases, release)
	return release, nil
}
func (f *fakeStore) ListAgentReleases(context.Context) ([]AgentRelease, error) {
	return f.releases, nil
}
func (f *fakeStore) DeleteAgentRelease(_ context.Context, releaseID string) (AgentRelease, error) {
	if f.deleteReleaseErr != nil {
		return AgentRelease{}, f.deleteReleaseErr
	}
	for index, release := range f.releases {
		if release.ID == releaseID {
			f.releases = append(f.releases[:index], f.releases[index+1:]...)
			return release, nil
		}
	}
	return AgentRelease{}, ErrNotFound
}
func (f *fakeStore) PrepareBootstrapAgentRelease(_ context.Context, nodeID, releaseID string) error {
	f.assignedReleaseID = releaseID
	f.updateState = NodeAgentUpdateState{NodeID: nodeID, State: "pending"}
	for index := range f.releases {
		if f.releases[index].ID == releaseID {
			f.updateState.DesiredRelease = &f.releases[index]
			return nil
		}
	}
	return ErrNotFound
}
func (f *fakeStore) AssignAgentRelease(_ context.Context, nodeID, releaseID string, expectedActualSequence, expectedDesiredSequence int64) (NodeAgentUpdateState, error) {
	f.assignedReleaseID = releaseID
	f.assignExpectedActual = expectedActualSequence
	f.assignExpectedDesired = expectedDesiredSequence
	if f.assignReleaseErr != nil {
		return NodeAgentUpdateState{}, f.assignReleaseErr
	}
	f.updateState = NodeAgentUpdateState{NodeID: nodeID, State: "pending"}
	for index := range f.releases {
		if f.releases[index].ID == releaseID {
			f.updateState.DesiredRelease = &f.releases[index]
		}
	}
	return f.updateState, nil
}
func (f *fakeStore) GetAgentUpdateState(_ context.Context, nodeID string) (NodeAgentUpdateState, error) {
	if f.updateState.NodeID == "" {
		f.updateState = NodeAgentUpdateState{NodeID: nodeID, State: "idle"}
	}
	return f.updateState, nil
}
func (f *fakeStore) GetAssignedAgentRelease(_ context.Context, token string, identity AgentCredentialIdentity, sequence int64) (AgentRelease, error) {
	for _, release := range f.releases {
		if release.Sequence == sequence {
			return release, nil
		}
	}
	return AgentRelease{}, ErrNotFound
}
func (f *fakeStore) UpdateNode(_ context.Context, id, name, address string, metadata map[string]any) (Node, error) {
	return Node{ID: id, Name: name, Address: address, Metadata: metadata}, nil
}
func (f *fakeStore) DeleteNode(context.Context, string) error            { return nil }
func (f *fakeStore) ListRoutes(context.Context, string) ([]Route, error) { return f.routes, nil }
func (f *fakeStore) ReorderRoutes(_ context.Context, _ string, _ []string) ([]Route, error) {
	return f.routes, nil
}
func (f *fakeStore) GetHAProxyControl(_ context.Context, nodeID string) (NodeHAProxyControl, error) {
	return NodeHAProxyControl{NodeID: nodeID, DesiredEnabled: true}, nil
}
func (f *fakeStore) UpdateHAProxyControl(_ context.Context, nodeID string, enabled bool, generation int64) (NodeHAProxyControl, error) {
	return NodeHAProxyControl{NodeID: nodeID, DesiredEnabled: enabled, Generation: generation + 1}, nil
}
func (f *fakeStore) RestartHAProxy(_ context.Context, nodeID string, generation int64) (NodeHAProxyControl, error) {
	f.restartNodeID, f.restartGeneration = nodeID, generation
	if f.restartErr != nil {
		return NodeHAProxyControl{}, f.restartErr
	}
	return NodeHAProxyControl{NodeID: nodeID, Supported: true, DesiredEnabled: true, Generation: generation, RestartGeneration: 1}, nil
}
func (f *fakeStore) CreateRoute(_ context.Context, nodeID string, spec RouteSpec) (Route, error) {
	spec.Enabled = false
	f.createdRouteFor = nodeID
	f.createdRoute = spec
	route := routeFromSpec(testRouteID, nodeID, spec)
	route.DeploymentState = "draft"
	return route, f.routeErr
}
func (f *fakeStore) GetRoute(context.Context, string, string) (Route, error) {
	if len(f.routes) > 0 {
		return f.routes[0], f.routeErr
	}
	return Route{ID: testRouteID}, f.routeErr
}
func (f *fakeStore) UpdateRoute(_ context.Context, nodeID, id string, spec RouteSpec) (Route, error) {
	f.updatedRouteFor, f.updatedRouteID, f.updatedRoute = nodeID, id, spec
	route := routeFromSpec(id, nodeID, spec)
	if spec.Enabled {
		route.DeploymentState = "pending"
	} else {
		route.DeploymentState = "draft"
	}
	return route, f.routeErr
}
func (f *fakeStore) SetRouteEnabled(_ context.Context, nodeID, id string, enabled bool, expectedVersion *int64) (Route, error) {
	if f.routeErr != nil {
		return Route{}, f.routeErr
	}
	for _, route := range f.routes {
		if route.ID != id {
			continue
		}
		if expectedVersion != nil && route.Version != *expectedVersion {
			return Route{}, ErrRouteVersionConflict
		}
		spec := routeAsSpec(route, enabled)
		f.updatedRouteFor, f.updatedRouteID, f.updatedRoute = nodeID, id, spec
		route.Enabled = enabled
		route.Version++
		return route, nil
	}
	return Route{}, ErrNotFound
}
func (f *fakeStore) GetNodeAgentVersion(context.Context, string) (string, error) {
	return f.agentVersion, nil
}
func (f *fakeStore) GetNodeRenderFacts(context.Context, string) (NodeRenderFacts, error) {
	return f.renderFacts, nil
}
func (f *fakeStore) DeleteRoute(_ context.Context, nodeID, id string, expectedVersion *int64) (RouteDeleteResult, error) {
	f.deletedRouteFor, f.deletedRouteID = nodeID, id
	f.deleteExpectedVersion = expectedVersion
	return f.deleteRouteResult, f.routeErr
}

func routeFromSpec(id, nodeID string, spec RouteSpec) Route {
	return Route{
		ID: id, NodeID: nodeID, Name: spec.Name, Version: 1, ListenerIP: spec.ListenerIP, ListenerPort: spec.ListenerPort,
		MatchMode: spec.MatchMode, SNIs: spec.SNIs, Fallback: spec.Fallback, Hostname: spec.Hostname, TargetType: spec.TargetType,
		TargetHost: spec.TargetHost, TargetPort: spec.TargetPort, UnixSocketPath: spec.UnixSocketPath,
		HealthCheck: spec.HealthCheck, ProxyProtocol: spec.ProxyProtocol, QuotaBytes: spec.QuotaBytes, QuotaAction: spec.QuotaAction,
		QuotaPeriod: spec.QuotaPeriod, ClientUploadMbps: spec.ClientUploadMbps, ClientDownloadMbps: spec.ClientDownloadMbps, Enabled: spec.Enabled,
		DeploymentState: "draft", DesiredFingerprint: routeSpecFingerprint(spec), CustomFragment: spec.CustomFragment,
		ClientIPv6: boolPointer(!spec.ClientIPv4Only),
	}
}
func (f *fakeStore) CreateEnrollmentToken(_ context.Context, nodeID, hash, prefix string, expires time.Time) (EnrollmentToken, error) {
	f.enrollmentFor, f.enrollmentHash = nodeID, hash
	return EnrollmentToken{ID: testRouteID, NodeID: nodeID, Prefix: prefix, ExpiresAt: expires}, nil
}
func (f *fakeStore) EnrollmentTokenUsed(context.Context, string) (bool, error) {
	if f.credentialUseErr != nil {
		return false, f.credentialUseErr
	}
	if f.credentialUsed != nil {
		return *f.credentialUsed, nil
	}
	return true, nil
}
func (f *fakeStore) RevokeEnrollmentToken(_ context.Context, tokenID string) error {
	f.revokeTokenCalls++
	f.revokedTokenID = tokenID
	return nil
}
func (f *fakeStore) RevokeOtherEnrollmentTokens(_ context.Context, nodeID, keepTokenID string) error {
	f.revokeOtherCalls++
	if f.revokeOtherFails > 0 {
		f.revokeOtherFails--
		return errors.New("temporary credential store failure")
	}
	f.revokedExceptID = keepTokenID
	return nil
}
func (f *fakeStore) AuthorizeCredentialRenewal(_ context.Context, token string, identity AgentCredentialIdentity, request CredentialRenewalRequest) (*CredentialRenewalRecord, error) {
	f.renewalToken, f.renewalIdentity, f.renewalRequest = token, identity, request
	if f.renewalErr != nil {
		return nil, f.renewalErr
	}
	if f.renewalExisting {
		record := f.renewalRecord
		return &record, nil
	}
	return nil, nil
}
func (f *fakeStore) CreateCredentialRenewal(_ context.Context, token string, identity AgentCredentialIdentity, candidate CredentialRenewalCandidate) (CredentialRenewalRecord, bool, error) {
	f.renewalToken, f.renewalIdentity, f.renewalCandidate = token, identity, candidate
	if f.renewalErr != nil {
		return CredentialRenewalRecord{}, false, f.renewalErr
	}
	record := f.renewalRecord
	if record.RenewalID == "" {
		record = CredentialRenewalRecord{
			ID: testRouteID, NodeID: identity.NodeID, RenewalID: candidate.RenewalID,
			CSRHash: candidate.CSRHash, CSRDER: candidate.CSRDER, NextTokenHash: candidate.NextTokenHash,
			NextTokenPrefix: candidate.NextTokenPrefix, CertificateSHA256: candidate.CertificateSHA256,
			CertificateSerial: candidate.CertificateSerial, CertificateDER: candidate.CertificateDER,
			CertificateNotAfter: candidate.CertificateNotAfter, ConfirmBy: candidate.ConfirmBy, CreatedAt: time.Now(),
		}
	}
	f.renewalRecord = record
	return record, !f.renewalExisting, nil
}
func (f *fakeStore) ConfirmCredentialRenewal(_ context.Context, token string, identity AgentCredentialIdentity, renewalID string) (CredentialRenewalRecord, error) {
	f.confirmToken, f.confirmIdentity, f.confirmedRenewalID = token, identity, renewalID
	if f.confirmErr != nil {
		return CredentialRenewalRecord{}, f.confirmErr
	}
	record := f.renewalRecord
	if record.RenewalID == "" {
		now := time.Now().UTC()
		record = CredentialRenewalRecord{RenewalID: renewalID, NodeID: identity.NodeID, ActivatedAt: &now}
	}
	return record, nil
}
func (f *fakeStore) AppendAudit(ctx context.Context, event AuditEvent) error {
	if f.appendAuditHook != nil {
		if err := f.appendAuditHook(ctx, event); err != nil {
			return err
		}
	}
	f.auditEvents = append(f.auditEvents, event)
	return nil
}
func (f *fakeStore) CleanupAudit(context.Context) error { return nil }
func (f *fakeStore) ListAudit(_ context.Context, nodeID string, limit int) ([]AuditEntry, error) {
	if limit < len(f.auditEntries) {
		return f.auditEntries[:limit], nil
	}
	return f.auditEntries, nil
}
func (f *fakeStore) IngestHeartbeat(_ context.Context, token string, heartbeat Heartbeat) (HeartbeatResult, error) {
	f.heartbeatToken, f.heartbeat = token, heartbeat
	if f.heartbeatResult.NodeID == "" {
		f.heartbeatResult = HeartbeatResult{Status: "accepted", NodeID: testNodeID}
	}
	return f.heartbeatResult, f.heartbeatErr
}
func (f *fakeStore) CreateConfigRevision(_ context.Context, nodeID, config, note string, metadata map[string]any) (ConfigRevision, error) {
	f.createdConfig = configRevisionInput{Config: config, Note: note, Metadata: metadata}
	return ConfigRevision{ID: testRouteID, NodeID: nodeID, Revision: 1, Config: config, Note: note, Metadata: metadata, SHA256: strings.Repeat("a", 64)}, nil
}
func (f *fakeStore) ListConfigRevisions(context.Context, string) ([]ConfigRevision, error) {
	return []ConfigRevision{{NodeID: testNodeID, Revision: 1}}, nil
}
func (f *fakeStore) GetConfigRevision(_ context.Context, nodeID string, revision int64) (ConfigRevision, error) {
	return ConfigRevision{NodeID: nodeID, Revision: revision}, nil
}
func (f *fakeStore) AssignDesiredRevision(_ context.Context, nodeID string, revision int64) (NodeConfigState, error) {
	f.desiredRevision = revision
	return NodeConfigState{NodeID: nodeID, DesiredRevision: &revision, State: "pending"}, nil
}
func (f *fakeStore) GetConfigState(context.Context, string) (NodeConfigState, error) {
	return NodeConfigState{NodeID: testNodeID, State: "unassigned"}, nil
}
func (f *fakeStore) IngestApplyReport(_ context.Context, token string, report ApplyReport) (NodeConfigState, error) {
	f.applyToken, f.applyReport = token, report
	return NodeConfigState{NodeID: testNodeID, DesiredRevision: &report.Revision, ActualRevision: &report.Revision, State: "in_sync"}, f.applyErr
}

func handler(f *fakeStore) http.Handler { return NewHandler(f, Config{AdminToken: testAdminToken}) }
func request(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHealth(t *testing.T) {
	w := request(t, handler(&fakeStore{}), "GET", "/healthz", "", "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"status":"ok","version":"dev"}`, w.Body.String())
	f := &fakeStore{healthErr: errors.New("down")}
	w = request(t, handler(f), "GET", "/healthz", "", "")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSuccessfulAdminMutationCreatesSanitizedAuditEvent(t *testing.T) {
	f := &fakeStore{}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes", `{"name":"edge-1","address":"192.0.2.10"}`, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code)
	require.Len(t, f.auditEvents, 1)
	event := f.auditEvents[0]
	assert.Equal(t, "node.create", event.Action)
	assert.Equal(t, "node", event.ResourceType)
	assert.Equal(t, "admin_token", event.ActorType)
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), testAdminToken)
	assert.NotContains(t, string(encoded), "edge-1")
}

func TestFailedMutationIsNotAuditedAndNodeAuditIsReadable(t *testing.T) {
	f := &fakeStore{auditEntries: []AuditEntry{{ID: 7, Action: "route.change", ResourceType: "route", Details: map[string]any{"node_id": testNodeID}}}}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes", `{"name":"bad","address":"not-an-ip"}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, f.auditEvents)

	w = request(t, handler(f), http.MethodGet, "/api/v1/nodes/"+testNodeID+"/audit?limit=10", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "route.change")
}

func TestAdminAuth(t *testing.T) {
	h := handler(&fakeStore{})
	w := request(t, h, "GET", "/api/v1/nodes", "", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.JSONEq(t, `{"error":{"code":"unauthorized","message":"valid bearer token required"}}`, w.Body.String())
	w = request(t, h, "GET", "/api/v1/nodes", "", testAdminToken)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHostKeyScanValidation(t *testing.T) {
	h := handler(&fakeStore{})
	w := request(t, h, http.MethodPost, "/api/v1/bootstrap/host-key", `{"address":"not-an-ip","ssh_port":22}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestBrowserLoginSessionAndLogout(t *testing.T) {
	h := handler(&fakeStore{})
	login := httptest.NewRequest(http.MethodPost, "http://panel.test/auth/login", strings.NewReader(`{"token":"`+testAdminToken+`"}`))
	login.Header.Set("Origin", "http://panel.test")
	login.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, login)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, w.Result().Cookies(), 1)
	cookie := w.Result().Cookies()[0]
	assert.Equal(t, sessionCookieName, cookie.Name)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.NotEqual(t, testAdminToken, cookie.Value)

	sessionReq := httptest.NewRequest(http.MethodGet, "http://panel.test/auth/session", nil)
	sessionReq.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, sessionReq)
	assert.Equal(t, http.StatusOK, w.Code)

	apiReq := httptest.NewRequest(http.MethodGet, "http://panel.test/api/v1/nodes", nil)
	apiReq.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, apiReq)
	assert.Equal(t, http.StatusOK, w.Code)

	logout := httptest.NewRequest(http.MethodPost, "http://panel.test/auth/logout", nil)
	logout.Header.Set("Origin", "http://panel.test")
	logout.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, logout)
	assert.Equal(t, http.StatusNoContent, w.Code)

	sessionReq = httptest.NewRequest(http.MethodGet, "http://panel.test/auth/session", nil)
	sessionReq.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, sessionReq)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestBrowserSessionMutationRequiresSameOrigin(t *testing.T) {
	h := handler(&fakeStore{})
	login := httptest.NewRequest(http.MethodPost, "http://panel.test/auth/login", strings.NewReader(`{"token":"`+testAdminToken+`"}`))
	login.Header.Set("Origin", "http://panel.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, login)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cookie := w.Result().Cookies()[0]

	mutation := httptest.NewRequest(http.MethodPost, "http://panel.test/api/v1/nodes", strings.NewReader(`{"name":"edge","address":"192.0.2.20"}`))
	mutation.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, mutation)
	assert.Equal(t, http.StatusForbidden, w.Code)

	mutation = httptest.NewRequest(http.MethodPost, "http://panel.test/api/v1/nodes", strings.NewReader(`{"name":"edge","address":"192.0.2.20"}`))
	mutation.AddCookie(cookie)
	mutation.Header.Set("Origin", "http://panel.test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, mutation)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestBrowserActivityRequiresSameOriginAndValidSession(t *testing.T) {
	h := handler(&fakeStore{})
	login := httptest.NewRequest(http.MethodPost, "http://panel.test/auth/login", strings.NewReader(`{"token":"`+testAdminToken+`"}`))
	login.Header.Set("Origin", "http://panel.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, login)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cookie := w.Result().Cookies()[0]

	activity := httptest.NewRequest(http.MethodPost, "http://panel.test/auth/activity", nil)
	activity.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, activity)
	assert.Equal(t, http.StatusForbidden, w.Code)

	activity = httptest.NewRequest(http.MethodPost, "http://panel.test/auth/activity", nil)
	activity.Header.Set("Origin", "http://panel.test")
	activity.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, activity)
	assert.Equal(t, http.StatusNoContent, w.Code)

	activity = httptest.NewRequest(http.MethodPost, "http://panel.test/auth/activity", nil)
	activity.Header.Set("Origin", "http://panel.test")
	activity.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "unknown"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, activity)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminPollingDoesNotExtendBrowserInactivity(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	settings := defaultPanelSettings()
	settings.InactivityTimeoutMinutes = 5
	sessions := newSessionStore(settings)
	sessions.now = func() time.Time { return now }
	sessions.put("polling-session")
	a := &API{sessions: sessions, adminToken: testAdminToken}
	h := a.admin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	now = now.Add(4 * time.Minute)
	poll := httptest.NewRequest(http.MethodGet, "http://panel.test/api/v1/nodes", nil)
	poll.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "polling-session"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, poll)
	assert.Equal(t, http.StatusNoContent, w.Code)

	now = now.Add(2 * time.Minute)
	poll = httptest.NewRequest(http.MethodGet, "http://panel.test/api/v1/nodes", nil)
	poll.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "polling-session"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, poll)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestLoginRejectsCrossOriginAndBadToken(t *testing.T) {
	h := handler(&fakeStore{})
	r := httptest.NewRequest(http.MethodPost, "http://panel.test/auth/login", strings.NewReader(`{"token":"`+testAdminToken+`"}`))
	r.Header.Set("Origin", "http://evil.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)

	r = httptest.NewRequest(http.MethodPost, "http://panel.test/auth/login", strings.NewReader(`{"token":"wrong"}`))
	r.Header.Set("Origin", "http://panel.test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Result().Cookies())
}

func TestBrowserLoginUsesConfiguredPublicOriginBehindProxy(t *testing.T) {
	h := NewHandler(&fakeStore{}, Config{AdminToken: testAdminToken, PublicURL: "https://panel.example"})
	r := httptest.NewRequest(http.MethodPost, "http://panel-internal:8080/auth/login", strings.NewReader(`{"token":"`+testAdminToken+`"}`))
	r.Header.Set("Origin", "https://panel.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, w.Result().Cookies(), 1)
	assert.True(t, w.Result().Cookies()[0].Secure)
}

func TestPanelSettingsGetPutRuntimeFactsAndAudit(t *testing.T) {
	f := &fakeStore{panelSettings: PanelSettings{
		Theme: "dark", Accent: "#22C55E", InactivityTimeoutMinutes: 30, MaxSessions: 5, AuditRetentionDays: 90,
	}}
	h := NewHandler(f, Config{
		AdminToken:         testAdminToken,
		PublicURL:          "https://panel.example",
		AgentPublicURL:     "https://agent.example:9443",
		ListenAddr:         "127.0.0.1:8080",
		AgentTLSListenAddr: ":4200",
	})
	w := request(t, h, http.MethodGet, "/api/v1/settings", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{
		"theme":"dark","accent":"#22C55E","session_timeout_minutes":30,
		"max_sessions":5,"audit_retention_days":90,"updated_at":"0001-01-01T00:00:00Z",
		"public_url":"https://panel.example","web_port":443,"agent_port":9443,
		"metrics_enabled":false,"metrics_allow_cidrs":0
	}`, w.Body.String())

	w = request(t, h, http.MethodPut, "/api/v1/settings", `{
		"theme":"system","accent":"#62D36F","session_timeout_minutes":45,
		"max_sessions":3,"audit_retention_days":365
	}`, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "system", f.updatedPanelSettings.Theme)
	assert.Equal(t, "#62D36F", f.updatedPanelSettings.Accent)
	assert.Equal(t, 45, f.updatedPanelSettings.InactivityTimeoutMinutes)
	assert.Equal(t, 3, f.updatedPanelSettings.MaxSessions)
	assert.Equal(t, 365, f.updatedPanelSettings.AuditRetentionDays)
	require.Len(t, f.auditEvents, 1)
	assert.Equal(t, "settings.update", f.auditEvents[0].Action)
	assert.Equal(t, "panel_settings", f.auditEvents[0].ResourceType)
	assert.Equal(t, "primary", f.auditEvents[0].ResourceID)
	assert.NotContains(t, w.Body.String(), `"public_url":""`)
}

func TestPanelSettingsStrictValidation(t *testing.T) {
	valid := `"theme":"dark","accent":"#22C55E","session_timeout_minutes":30,"max_sessions":5,"audit_retention_days":90`
	tests := map[string]string{
		"theme":          `{"theme":"light","accent":"#22C55E","session_timeout_minutes":30,"max_sessions":5,"audit_retention_days":90}`,
		"accent":         `{"theme":"dark","accent":"#12345G","session_timeout_minutes":30,"max_sessions":5,"audit_retention_days":90}`,
		"timeout low":    `{"theme":"dark","accent":"#22C55E","session_timeout_minutes":4,"max_sessions":5,"audit_retention_days":90}`,
		"timeout high":   `{"theme":"dark","accent":"#22C55E","session_timeout_minutes":1441,"max_sessions":5,"audit_retention_days":90}`,
		"sessions low":   `{"theme":"dark","accent":"#22C55E","session_timeout_minutes":30,"max_sessions":0,"audit_retention_days":90}`,
		"sessions high":  `{"theme":"dark","accent":"#22C55E","session_timeout_minutes":30,"max_sessions":101,"audit_retention_days":90}`,
		"retention low":  `{"theme":"dark","accent":"#22C55E","session_timeout_minutes":30,"max_sessions":5,"audit_retention_days":6}`,
		"retention high": `{"theme":"dark","accent":"#22C55E","session_timeout_minutes":30,"max_sessions":5,"audit_retention_days":3651}`,
		"runtime field":  `{` + valid + `,"public_url":"https://evil.example"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			f := &fakeStore{}
			w := request(t, handler(f), http.MethodPut, "/api/v1/settings", body, testAdminToken)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Empty(t, f.auditEvents)
		})
	}
}

func TestPanelSettingsMaxSessionsAppliesToActiveBrowserSessions(t *testing.T) {
	f := &fakeStore{panelSettings: PanelSettings{
		Theme: "dark", Accent: "#22C55E", InactivityTimeoutMinutes: 30, MaxSessions: 3, AuditRetentionDays: 90,
	}}
	h := NewHandler(f, Config{AdminToken: testAdminToken})
	cookies := make([]*http.Cookie, 0, 3)
	for range 3 {
		login := httptest.NewRequest(http.MethodPost, "http://panel.test/auth/login", strings.NewReader(`{"token":"`+testAdminToken+`"}`))
		login.Header.Set("Origin", "http://panel.test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, login)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		cookies = append(cookies, w.Result().Cookies()[0])
	}

	w := request(t, h, http.MethodPut, "/api/v1/settings", `{
		"theme":"dark","accent":"#22C55E","session_timeout_minutes":30,
		"max_sessions":1,"audit_retention_days":90
	}`, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	for index, cookie := range cookies {
		sessionRequest := httptest.NewRequest(http.MethodGet, "http://panel.test/auth/session", nil)
		sessionRequest.AddCookie(cookie)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, sessionRequest)
		if index == len(cookies)-1 {
			assert.Equal(t, http.StatusOK, w.Code)
		} else {
			assert.Equal(t, http.StatusUnauthorized, w.Code)
		}
	}
}

func TestPanelSettingsReadFailureUsesStrictStartupSessionLimit(t *testing.T) {
	f := &fakeStore{panelSettingsErr: errors.New("settings read failed")}
	h := NewHandler(f, Config{AdminToken: testAdminToken})
	cookies := make([]*http.Cookie, 0, 2)
	for range 2 {
		login := httptest.NewRequest(http.MethodPost, "http://panel.test/auth/login", strings.NewReader(`{"token":"`+testAdminToken+`"}`))
		login.Header.Set("Origin", "http://panel.test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, login)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		cookies = append(cookies, w.Result().Cookies()[0])
	}

	for index, cookie := range cookies {
		sessionRequest := httptest.NewRequest(http.MethodGet, "http://panel.test/auth/session", nil)
		sessionRequest.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, sessionRequest)
		if index == 0 {
			assert.Equal(t, http.StatusUnauthorized, w.Code)
		} else {
			assert.Equal(t, http.StatusOK, w.Code)
		}
	}
}

func TestSessionStoreInactivityAndDeterministicOldestEviction(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	settings := defaultPanelSettings()
	settings.InactivityTimeoutMinutes = 10
	settings.MaxSessions = 2
	store := newSessionStore(settings)
	store.now = func() time.Time { return now }

	store.put("first")
	now = now.Add(time.Second)
	store.put("second")
	now = now.Add(time.Second)
	store.put("third")
	assert.False(t, store.check("first"))
	assert.True(t, store.check("second"))
	assert.True(t, store.check("third"))

	settings.MaxSessions = 1
	store.configure(settings)
	assert.False(t, store.check("second"))
	assert.True(t, store.check("third"))

	settings.InactivityTimeoutMinutes = 5
	store.configure(settings)
	now = now.Add(4 * time.Minute)
	assert.True(t, store.touch("third"))
	now = now.Add(5 * time.Minute)
	assert.False(t, store.check("third"))
}

func TestSessionStorePassiveChecksDoNotExtendInactivity(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	settings := defaultPanelSettings()
	settings.InactivityTimeoutMinutes = 5
	store := newSessionStore(settings)
	store.now = func() time.Time { return now }
	store.put("passive")

	now = now.Add(4 * time.Minute)
	assert.True(t, store.check("passive"))
	now = now.Add(2 * time.Minute)
	assert.False(t, store.check("passive"))
}

func TestSessionStoreTouchChecksOldDeadlineBeforeExtending(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	settings := defaultPanelSettings()
	settings.InactivityTimeoutMinutes = 5
	store := newSessionStore(settings)
	store.now = func() time.Time { return now }
	store.put("active")
	store.put("expired")

	now = now.Add(4 * time.Minute)
	assert.True(t, store.touch("active"))
	now = now.Add(2 * time.Minute)
	assert.True(t, store.check("active"))
	assert.False(t, store.touch("expired"), "an expired session must not be revived")
}

func TestSessionStoreConfigureExpiresUsingOldPolicyFirst(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	settings := defaultPanelSettings()
	settings.InactivityTimeoutMinutes = 5
	store := newSessionStore(settings)
	store.now = func() time.Time { return now }
	store.put("expired-under-old-policy")

	now = now.Add(6 * time.Minute)
	settings.InactivityTimeoutMinutes = 30
	store.configure(settings)
	assert.False(t, store.check("expired-under-old-policy"), "a relaxed new policy must not revive an expired session")

	settings.InactivityTimeoutMinutes = 30
	store = newSessionStore(settings)
	store.now = func() time.Time { return now }
	store.put("expired-under-new-policy")
	now = now.Add(10 * time.Minute)
	settings.InactivityTimeoutMinutes = 5
	store.configure(settings)
	assert.False(t, store.check("expired-under-new-policy"), "a stricter new policy must apply immediately")
}

func TestSessionStoreRecentlyActiveSessionSurvivesLimitEviction(t *testing.T) {
	now := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	settings := defaultPanelSettings()
	settings.MaxSessions = 2
	store := newSessionStore(settings)
	store.now = func() time.Time { return now }

	store.put("older-active")
	now = now.Add(time.Second)
	store.put("newer-idle")
	now = now.Add(time.Second)
	require.True(t, store.touch("older-active"))
	now = now.Add(time.Second)
	store.put("newest")

	assert.True(t, store.check("older-active"))
	assert.False(t, store.check("newer-idle"))
	assert.True(t, store.check("newest"))
}

func TestEmbeddedWebAndOperationalDetail(t *testing.T) {
	now := time.Now()
	averageIn, averageOut, averageUsed := 25.5, 14.5, 40.0
	f := &fakeStore{operational: NodeOperationalDetail{
		Node: Node{ID: testNodeID, Name: "edge"}, RoutesTotal: 3, RoutesEnabled: 2,
		TrafficMonth: "2026-07", TrafficBytesIn: 100, TrafficBytesOut: 200, TrafficUsed: 300, TrafficObserved: true,
		TrafficDay: "2026-07-13", TrafficDayBytesIn: 30, TrafficDayBytesOut: 20, TrafficDayUsed: 50, TrafficDayObserved: true,
		TrafficDailyAverageBytesIn: &averageIn, TrafficDailyAverageBytesOut: &averageOut,
		TrafficDailyAverageUsed: &averageUsed, TrafficDailyObservedDays: 12,
		LatestHeartbeat: &NodeHeartbeat{AgentVersion: "0.1.0", Status: "online", Metrics: map[string]any{"load": 0.5}, ReceivedAt: now},
	}}
	h := handler(f)
	w := request(t, h, http.MethodGet, "/", "", "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "NodeFlow")
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Contains(t, w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
	w = request(t, h, http.MethodGet, "/nodes/detail", "", "")
	assert.Equal(t, http.StatusOK, w.Code)
	w = request(t, h, http.MethodGet, "/api/v1/nodes/"+testNodeID+"/operational", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"routes_total":3`)
	assert.Contains(t, w.Body.String(), `"agent_version":"0.1.0"`)
	assert.Contains(t, w.Body.String(), `"traffic_used_bytes":300`)
	assert.Contains(t, w.Body.String(), `"traffic_observed":true`)
	assert.Contains(t, w.Body.String(), `"traffic_day":"2026-07-13"`)
	assert.Contains(t, w.Body.String(), `"traffic_day_used_bytes":50`)
	assert.Contains(t, w.Body.String(), `"traffic_daily_average_used_bytes":40`)
	assert.Contains(t, w.Body.String(), `"traffic_daily_observed_days":12`)
}

func TestCreateNode(t *testing.T) {
	f := &fakeStore{}
	w := request(t, handler(f), "POST", "/api/v1/nodes", `{"name":"edge-1","address":"192.0.2.10","metadata":{"region":"eu"}}`, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, "edge-1", f.createdNode.Name)
	assert.Equal(t, "192.0.2.10", f.createdNode.Address)
	assert.Equal(t, "eu", f.createdNode.Metadata["region"])
	w = request(t, handler(f), "POST", "/api/v1/nodes", `{"name":"bad","address":"not-an-ip"}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestNodeFirewallPolicy(t *testing.T) {
	f := &fakeStore{firewallPolicy: NodeFirewallPolicy{NodeID: testNodeID, Mode: "observe"}}
	w := request(t, handler(f), http.MethodGet, "/api/v1/nodes/"+testNodeID+"/firewall", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"mode":"observe"`)

	w = request(t, handler(f), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/firewall", `{"mode":"off"}`, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "off", f.firewallPolicy.Mode)

	w = request(t, handler(f), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/firewall", `{"mode":"enable"}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAgentReleaseAssignmentRejectsUnknownOrMismatchedPlatform(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "unknown node platform", err: ErrReleasePlatformUnknown, code: "node_platform_unknown"},
		{name: "platform mismatch", err: ErrReleasePlatform, code: "platform_mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{assignReleaseErr: test.err}
			body := `{"release_id":"33333333-3333-4333-8333-333333333333","expected_actual_sequence":4,"expected_desired_sequence":4}`
			w := request(t, handler(store), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/agent-update", body, testAdminToken)

			assert.Equal(t, http.StatusConflict, w.Code)
			assert.Contains(t, w.Body.String(), `"code":"`+test.code+`"`)
		})
	}
}

func TestAgentReleaseAssignmentRequiresAndForwardsCASState(t *testing.T) {
	releaseID := "33333333-3333-4333-8333-333333333333"
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing actual sequence", body: `{"release_id":"` + releaseID + `","expected_desired_sequence":0}`},
		{name: "missing desired sequence", body: `{"release_id":"` + releaseID + `","expected_actual_sequence":0}`},
		{name: "negative actual sequence", body: `{"release_id":"` + releaseID + `","expected_actual_sequence":-1,"expected_desired_sequence":0}`},
		{name: "negative desired sequence", body: `{"release_id":"` + releaseID + `","expected_actual_sequence":0,"expected_desired_sequence":-1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := request(t, handler(&fakeStore{}), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/agent-update", test.body, testAdminToken)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), `"code":"validation_error"`)
		})
	}

	store := &fakeStore{}
	body := `{"release_id":"` + releaseID + `","expected_actual_sequence":7,"expected_desired_sequence":8}`
	w := request(t, handler(store), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/agent-update", body, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, releaseID, store.assignedReleaseID)
	assert.Equal(t, int64(7), store.assignExpectedActual)
	assert.Equal(t, int64(8), store.assignExpectedDesired)
}

func TestAgentReleaseAssignmentReturnsStableConflictForStaleCASState(t *testing.T) {
	store := &fakeStore{assignReleaseErr: ErrAgentUpdateStateChanged}
	body := `{"release_id":"33333333-3333-4333-8333-333333333333","expected_actual_sequence":4,"expected_desired_sequence":5}`
	w := request(t, handler(store), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/agent-update", body, testAdminToken)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"update_state_changed"`)
}

func TestRouteIsScopedToNode(t *testing.T) {
	f := &fakeStore{}
	body := `{"hostname":"VPN.Example.COM.","target_host":"127.0.0.1","target_port":8443}`
	w := request(t, handler(f), "POST", "/api/v1/nodes/"+testNodeID+"/routes", body, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, testNodeID, f.createdRouteFor)
	assert.Equal(t, "vpn.example.com", f.createdRoute.Hostname)
	assert.Equal(t, "vpn.example.com", f.createdRoute.Name)
	assert.Equal(t, "sni", f.createdRoute.MatchMode)
	assert.Equal(t, []string{"vpn.example.com"}, f.createdRoute.SNIs)
	assert.Equal(t, "*", f.createdRoute.ListenerIP)
	assert.Equal(t, 443, f.createdRoute.ListenerPort)
	assert.Equal(t, "tcp", f.createdRoute.TargetType)
	assert.Equal(t, "none", f.createdRoute.ProxyProtocol)
	assert.Equal(t, "calendar_month", f.createdRoute.QuotaPeriod)
	assert.True(t, f.createdRoute.HealthCheck)
	assert.False(t, f.createdRoute.Enabled, "new routes must always be saved as drafts")
	assert.Contains(t, w.Body.String(), `"deployment_state":"draft"`)
}

func TestCreateMultiSNITCPRoute(t *testing.T) {
	f := &fakeStore{}
	body := `{
		"listener_ip":"2001:db8::10","listener_port":8443,
		"snis":["VPN.Example.COM.","cdn.example.com"],
		"target_type":"tcp","target_host":"ORIGIN.Example.COM.","target_port":10443,
		"proxy_protocol":"v2","quota_bytes":1073741824,"quota_action":"block_new","quota_period":"daily","client_upload_mbps":200,"client_download_mbps":500,
		"custom_fragment":"  timeout connect 5s\n"
	}`
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/routes", body, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, "2001:db8::10", f.createdRoute.ListenerIP)
	assert.Equal(t, []string{"vpn.example.com", "cdn.example.com"}, f.createdRoute.SNIs)
	assert.Equal(t, "vpn.example.com", f.createdRoute.Hostname)
	assert.Equal(t, "origin.example.com", f.createdRoute.TargetHost)
	assert.Equal(t, "v2", f.createdRoute.ProxyProtocol)
	require.NotNil(t, f.createdRoute.QuotaBytes)
	assert.Equal(t, int64(1073741824), *f.createdRoute.QuotaBytes)
	assert.Equal(t, "block_new", f.createdRoute.QuotaAction)
	assert.Equal(t, "daily", f.createdRoute.QuotaPeriod)
	require.NotNil(t, f.createdRoute.ClientUploadMbps)
	assert.Equal(t, int64(200), *f.createdRoute.ClientUploadMbps)
	require.NotNil(t, f.createdRoute.ClientDownloadMbps)
	assert.Equal(t, int64(500), *f.createdRoute.ClientDownloadMbps)
	assert.Contains(t, w.Body.String(), `"snis":["vpn.example.com","cdn.example.com"]`)
}

func TestRouteV6FieldsRoundTrip(t *testing.T) {
	f := &fakeStore{}
	body := `{
		"name":"IPv4 ingress","listener_ip":"192.0.2.44","listener_port":443,
		"match_mode":"destination_ip","snis":[],"fallback":true,
		"target_type":"tcp","target_host":"198.51.100.10","target_port":8443,
		"health_check":false,"proxy_protocol":"v2","enabled":false
	}`
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/routes", body, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, "IPv4 ingress", f.createdRoute.Name)
	assert.Equal(t, "fallback", f.createdRoute.MatchMode, "destination_ip normalizes to fallback")
	assert.True(t, f.createdRoute.Fallback)
	assert.False(t, f.createdRoute.HealthCheck)
	assert.Contains(t, w.Body.String(), `"name":"IPv4 ingress"`)
	assert.Contains(t, w.Body.String(), `"match_mode":"fallback"`, "API returns canonical fallback mode")
	assert.Contains(t, w.Body.String(), `"health_check":false`)

	update := strings.Replace(body, `"enabled":false`, `"enabled":true`, 1)
	w = request(t, handler(f), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, update, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "IPv4 ingress", f.updatedRoute.Name)
	assert.Equal(t, "fallback", f.updatedRoute.MatchMode, "destination_ip normalizes to fallback on update")
	assert.False(t, f.updatedRoute.HealthCheck)
	assert.True(t, f.updatedRoute.Enabled)
}

func TestQuotaActionValidation(t *testing.T) {
	for _, body := range []string{
		`{"snis":["vpn.example.com"],"target_host":"127.0.0.1","target_port":443,"quota_action":"block_new"}`,
		`{"snis":["vpn.example.com"],"target_host":"127.0.0.1","target_port":443,"quota_bytes":10,"quota_action":"drop_all"}`,
		`{"snis":["vpn.example.com"],"target_host":"127.0.0.1","target_port":443,"quota_bytes":10,"quota_period":"weekly"}`,
	} {
		w := request(t, handler(&fakeStore{}), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/routes", body, testAdminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestCreateFallbackUnixRoute(t *testing.T) {
	f := &fakeStore{}
	body := `{
		"listener_ip":"*","listener_port":443,"fallback":true,
		"target_type":"unix","unix_socket_path":"/dev/shm/xray.sock",
		"proxy_protocol":"v1","enabled":false
	}`
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/routes", body, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, "tcp-443", f.createdRoute.Name)
	assert.Equal(t, "fallback", f.createdRoute.MatchMode, "wildcard+fallback is the successor to any_tcp")
	assert.True(t, f.createdRoute.HealthCheck)
	assert.True(t, f.createdRoute.Fallback)
	assert.Empty(t, f.createdRoute.SNIs)
	assert.Empty(t, f.createdRoute.Hostname)
	assert.Equal(t, "unix", f.createdRoute.TargetType)
	assert.Equal(t, "/dev/shm/xray.sock", f.createdRoute.UnixSocketPath)
	assert.Empty(t, f.createdRoute.TargetHost)
	assert.Zero(t, f.createdRoute.TargetPort)
	assert.False(t, f.createdRoute.Enabled)
}

func TestRouteCRUD(t *testing.T) {
	port := 443
	spec := RouteSpec{
		ListenerIP: "*", ListenerPort: port, SNIs: []string{"old.example.com"},
		Hostname: "old.example.com", TargetType: "tcp", TargetHost: "192.0.2.20",
		TargetPort: 443, ProxyProtocol: "none", Enabled: true,
	}
	f := &fakeStore{routes: []Route{routeFromSpec(testRouteID, testNodeID, spec)}}
	h := handler(f)

	w := request(t, h, http.MethodGet, "/api/v1/nodes/"+testNodeID+"/routes", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"old.example.com"`)

	w = request(t, h, http.MethodGet, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	body := `{"listener_port":9443,"snis":["new.example.com"],"target_host":"192.0.2.30","target_port":443,"enabled":false}`
	w = request(t, h, http.MethodPut, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, body, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, testNodeID, f.updatedRouteFor)
	assert.Equal(t, testRouteID, f.updatedRouteID)
	assert.Equal(t, 9443, f.updatedRoute.ListenerPort)
	assert.Equal(t, []string{"new.example.com"}, f.updatedRoute.SNIs)
	assert.False(t, f.updatedRoute.Enabled)

	w = request(t, h, http.MethodDelete, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, "", testAdminToken)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, testNodeID, f.deletedRouteFor)
	assert.Equal(t, testRouteID, f.deletedRouteID)
}

func TestRouteConflictIs409(t *testing.T) {
	f := &fakeStore{routeErr: &pgconn.PgError{Code: "23505"}}
	body := `{"hostname":"vpn.example.com","target_host":"127.0.0.1","target_port":8443}`
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/routes", body, testAdminToken)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"already_exists"`)
}

func TestRouteValidationRejectsUnsafeAndAmbiguousInputs(t *testing.T) {
	port443, port0 := 443, 0
	zero, negative, tooLarge := int64(0), int64(-1), maxRouteClientRateMbps+1
	tests := []struct {
		name string
		in   routeInput
	}{
		{name: "bad listener", in: routeInput{ListenerIP: "not-an-ip", Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443}},
		{name: "listener control", in: routeInput{ListenerIP: "\n*", Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443}},
		{name: "bad listener port", in: routeInput{ListenerPort: &port0, Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443}},
		{name: "duplicate SNI", in: routeInput{ListenerPort: &port443, SNIs: []string{"a.example", "A.EXAMPLE."}, TargetHost: "127.0.0.1", TargetPort: 443}},
		{name: "SNI injection", in: routeInput{SNIs: []string{"a.example\nbackend evil"}, TargetHost: "127.0.0.1", TargetPort: 443}},
		{name: "leading SNI control", in: routeInput{SNIs: []string{"\ta.example"}, TargetHost: "127.0.0.1", TargetPort: 443}},
		{name: "fallback with SNI", in: routeInput{Fallback: true, SNIs: []string{"a.example"}, TargetHost: "127.0.0.1", TargetPort: 443}},
		{name: "bad match mode", in: routeInput{MatchMode: "host_header", Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443}},
		// Note: any_tcp and destination_ip are accepted (normalized to fallback), NOT rejected.
		{name: "SNI marked fallback", in: routeInput{MatchMode: "sni", Fallback: true, Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443}},
		{name: "missing SNI", in: routeInput{TargetHost: "127.0.0.1", TargetPort: 443}},
		{name: "bad target host", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1\nserver evil", TargetPort: 443}},
		{name: "relative unix", in: routeInput{Hostname: "a.example", TargetType: "unix", UnixSocketPath: "dev/shm/xray.sock"}},
		{name: "root unix", in: routeInput{Hostname: "a.example", TargetType: "unix", UnixSocketPath: "/"}},
		{name: "unclean unix", in: routeInput{Hostname: "a.example", TargetType: "unix", UnixSocketPath: "/dev/shm/../xray.sock"}},
		{name: "unix with TCP fields", in: routeInput{Hostname: "a.example", TargetType: "unix", TargetHost: "127.0.0.1", UnixSocketPath: "/dev/shm/xray.sock"}},
		{name: "bad proxy", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443, ProxyProtocol: "send-proxy"}},
		{name: "zero quota", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443, QuotaBytes: &zero}},
		{name: "negative quota", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443, QuotaBytes: &negative}},
		{name: "zero client upload rate", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443, ClientUploadMbps: &zero}},
		{name: "negative client download rate", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443, ClientDownloadMbps: &negative}},
		{name: "too large client upload rate", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443, ClientUploadMbps: &tooLarge}},
		{name: "control fragment", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443, CustomFragment: "  option tcplog\ra"}},
		{name: "escaped fragment", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443, CustomFragment: "timeout connect 5s\\"}},
		{name: "section fragment", in: routeInput{Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443, CustomFragment: "  backend injected"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateRoute(tt.in, false)
			assert.Error(t, err)
		})
	}
}

func TestRouteManualBackendDirectivesAreNormalized(t *testing.T) {
	spec, err := validateRoute(routeInput{
		Hostname: "a.example", TargetHost: "127.0.0.1", TargetPort: 443,
		CustomFragment: "\r\n\ttimeout connect 9s  \r\n\r\n option redispatch\r\n\r\n",
	}, false)
	require.NoError(t, err)
	assert.Equal(t, "    timeout connect 9s\n\n    option redispatch", spec.CustomFragment)
}

func TestEnrollmentReturnsPlaintextOnceAndStoresHash(t *testing.T) {
	f := &fakeStore{}
	w := request(t, handler(f), "POST", "/api/v1/nodes/"+testNodeID+"/enrollment-tokens", `{"ttl_seconds":600}`, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var payload map[string]any
	require.NoError(t, jsonDecode(w.Body.String(), &payload))
	token, ok := payload["token"].(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(token, "nfe_"))
	sum := sha256.Sum256([]byte(token))
	assert.Equal(t, hex.EncodeToString(sum[:]), f.enrollmentHash)
	assert.Equal(t, testNodeID, f.enrollmentFor)
}

func TestHeartbeatAuthAndIngest(t *testing.T) {
	f := &fakeStore{}
	h := handler(f)
	w := request(t, h, "POST", "/agent/v1/heartbeat", `{"version":"1.0.0","status":"online","metrics":{"load":1}}`, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = request(t, h, "POST", "/agent/v1/heartbeat", `{"version":"1.0.0","status":"online","metrics":{"load":1}}`, "enrollment-secret")
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Equal(t, "enrollment-secret", f.heartbeatToken)
	assert.Equal(t, "1.0.0", f.heartbeat.Version)
	assert.Equal(t, json.Number("1"), f.heartbeat.Metrics["load"])
	assert.NotContains(t, w.Body.String(), `"assignment"`)
}

func TestHeartbeatRequiresAndBindsMTLSNodeIdentity(t *testing.T) {
	f := &fakeStore{}
	h := NewHandler(f, Config{AdminToken: testAdminToken, RequireAgentMTLS: true})
	body := `{"version":"1.0.0","status":"online","metrics":{}}`

	w := request(t, h, http.MethodPost, "/agent/v1/heartbeat", body, "enrollment-secret")
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"mtls_required"`)
	assert.Empty(t, f.heartbeatToken)

	leaf := &x509.Certificate{Subject: pkix.Name{CommonName: testNodeID}}
	r := httptest.NewRequest(http.MethodPost, "/agent/v1/heartbeat", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer enrollment-secret")
	r.Header.Set("Content-Type", "application/json")
	r.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
		VerifiedChains:   [][]*x509.Certificate{{leaf}},
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Equal(t, testNodeID, f.heartbeat.MTLSNodeID)
}

func TestAgentMTLSRejectsInvalidCertificateIdentityBeforeStore(t *testing.T) {
	f := &fakeStore{}
	h := NewHandler(f, Config{AdminToken: testAdminToken, RequireAgentMTLS: true})
	leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "not-a-node-id"}}
	r := httptest.NewRequest(http.MethodPost, "/agent/v1/config-report", strings.NewReader(`{"revision":1,"state":"applied"}`))
	r.Header.Set("Authorization", "Bearer enrollment-secret")
	r.Header.Set("Content-Type", "application/json")
	r.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
		VerifiedChains:   [][]*x509.Certificate{{leaf}},
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	assert.Empty(t, f.applyToken)
}

func TestHeartbeatDecodesObservedConfigState(t *testing.T) {
	f := &fakeStore{}
	w := request(t, handler(f), http.MethodPost, "/agent/v1/heartbeat", `{
		"version":"1.0.0","status":"online","metrics":{},
		"actual_revision":42,
		"config_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}`, "enrollment-secret")
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.NotNil(t, f.heartbeat.ActualRevision)
	assert.Equal(t, int64(42), *f.heartbeat.ActualRevision)
	assert.Equal(t, strings.Repeat("a", 64), f.heartbeat.ConfigSHA256)
}

func TestValidateObservedConfig(t *testing.T) {
	revision := int64(1)
	assert.NoError(t, validateObservedConfig(Heartbeat{}))
	assert.NoError(t, validateObservedConfig(Heartbeat{ActualRevision: &revision, ConfigSHA256: strings.Repeat("a", 64)}))
	zero := int64(0)
	assert.ErrorIs(t, validateObservedConfig(Heartbeat{ActualRevision: &zero}), ErrInvalidObservedConfig)
	assert.ErrorIs(t, validateObservedConfig(Heartbeat{ConfigSHA256: "not-a-sha"}), ErrInvalidObservedConfig)
}

func TestHeartbeatReturnsOnlyAssignedRevision(t *testing.T) {
	f := &fakeStore{heartbeatResult: HeartbeatResult{
		Status: "accepted", NodeID: testNodeID,
		Assignment: &ConfigAssignment{Revision: 42, Config: "global\n  daemon\n", SHA256: strings.Repeat("a", 64)},
	}}
	w := request(t, handler(f), http.MethodPost, "/agent/v1/heartbeat", `{"version":"1.0.0","status":"online","metrics":{}}`, "enrollment-secret")
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"revision":42`)
	assert.Contains(t, w.Body.String(), `"config":"global\n  daemon\n"`)
}

func TestNodeTrafficMonthAndQuotaStatus(t *testing.T) {
	limit := int64(1000)
	f := &fakeStore{traffic: NodeTrafficReport{
		NodeID: testNodeID, Month: "2026-07", BytesIn: 700, BytesOut: 500, UsedBytes: 1200,
		Enforcement: false, Backends: []TrafficBackend{}, Routes: []TrafficRoute{{
			RouteID: testRouteID, BackendKey: RouteBackendKey(testRouteID), UsedBytes: 1200,
			LimitBytes: &limit, Reached: true, Enforcement: false, Observed: true,
		}},
	}}
	w := request(t, handler(f), http.MethodGet, "/api/v1/nodes/"+testNodeID+"/traffic?month=2026-07", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "2026-07", f.trafficMonth.Format("2006-01"))
	assert.Contains(t, w.Body.String(), `"reached":true`)
	assert.Contains(t, w.Body.String(), `"enforcement":false`)
}

func TestNodeTrafficRejectsInvalidMonth(t *testing.T) {
	h := handler(&fakeStore{})
	for _, path := range []string{
		"/api/v1/nodes/" + testNodeID + "/traffic?month=2026-13",
		"/api/v1/nodes/" + testNodeID + "/traffic?month=2026-7",
		"/api/v1/nodes/" + testNodeID + "/traffic?month=2026-07&month=2026-08",
	} {
		w := request(t, h, http.MethodGet, path, "", testAdminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code, path+": "+w.Body.String())
	}
}

func TestHeartbeatRejectsInvalidTrafficCounters(t *testing.T) {
	f := &fakeStore{heartbeatErr: ErrInvalidTrafficMetrics}
	w := request(t, handler(f), http.MethodPost, "/agent/v1/heartbeat", `{
		"version":"1.0.0","metrics":{"haproxy_runtime":{"bytes_in":-1,"bytes_out":2}}
	}`, "enrollment-secret")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"validation_error"`)
}

func TestConfigRevisionAndDesiredAssignment(t *testing.T) {
	f := &fakeStore{}
	h := handler(f)
	w := request(t, h, "POST", "/api/v1/nodes/"+testNodeID+"/config-revisions", `{"config":"global\n  daemon\n","note":"first","metadata":{"source":"routes"}}`, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, "global\n  daemon\n", f.createdConfig.Config)
	assert.Equal(t, "routes", f.createdConfig.Metadata["source"])
	w = request(t, h, "PUT", "/api/v1/nodes/"+testNodeID+"/desired-revision", `{"revision":1}`, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, int64(1), f.desiredRevision)
	w = request(t, h, "GET", "/api/v1/nodes/"+testNodeID+"/config-revisions/0", "", testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestConfigRevisionRejectsOversizedConfig(t *testing.T) {
	f := &fakeStore{}
	body, err := json.Marshal(configRevisionInput{Config: strings.Repeat("x", MaxManagedConfigBytes+1)})
	require.NoError(t, err)
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-revisions", string(body), testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, f.createdConfig.Config)
}

func TestConfigReportAuthValidationAndIngest(t *testing.T) {
	f := &fakeStore{}
	h := handler(f)
	body := `{"revision":1,"state":"applied","rollback_attempted":false,"details":{"haproxy":"ok"}}`
	w := request(t, h, "POST", "/agent/v1/config-report", body, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = request(t, h, "POST", "/agent/v1/config-report", body, "enrollment-secret")
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Equal(t, "enrollment-secret", f.applyToken)
	assert.Equal(t, int64(1), f.applyReport.Revision)
	assert.Equal(t, "ok", f.applyReport.Details["haproxy"])
	w = request(t, h, "POST", "/agent/v1/config-report", `{"revision":1,"state":"unknown"}`, "enrollment-secret")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = request(t, h, "POST", "/agent/v1/config-report", `{"revision":1,"state":"failed","error":"raw private diagnostic"}`, "enrollment-secret")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	oversized, err := json.Marshal(ApplyReport{Revision: 1, State: "failed", Details: map[string]any{"diagnostic": strings.Repeat("x", MaxReportDetailsBytes)}})
	require.NoError(t, err)
	w = request(t, h, http.MethodPost, "/agent/v1/config-report", string(oversized), "enrollment-secret")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRejectsUnknownJSONAndOversizedBody(t *testing.T) {
	h := handler(&fakeStore{})
	w := request(t, h, "POST", "/api/v1/nodes", `{"name":"n","address":"192.0.2.1","unknown":true}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = request(t, h, "POST", "/api/v1/nodes", strings.Repeat("x", (1<<20)+1), testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

type fakeInstaller struct {
	request       bootstrap.Request
	err           error
	rollbackErr   error
	finalizeErr   error
	rollbackCalls int
	finalizeCalls int
	finalizeHook  func()
}

type blockingBootstrapInstaller struct {
	started chan struct{}
	release chan struct{}
}

func (f *blockingBootstrapInstaller) Install(ctx context.Context, _ bootstrap.Request) error {
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeInstaller) Install(_ context.Context, r bootstrap.Request) error {
	f.request = r
	if r.OnReleaseSelected != nil && r.ReleaseID != "" {
		if err := r.OnReleaseSelected(context.Background(), r.ReleaseID); err != nil {
			return err
		}
	}
	return f.err
}

func (f *fakeInstaller) RollbackCredentials(context.Context, bootstrap.Request) error {
	f.rollbackCalls++
	return f.rollbackErr
}

func (f *fakeInstaller) FinalizeCredentials(context.Context, bootstrap.Request) error {
	f.finalizeCalls++
	if f.finalizeHook != nil {
		f.finalizeHook()
	}
	return f.finalizeErr
}

func startBootstrapJob(t *testing.T, h http.Handler, body string) (bootstrapJobView, string) {
	t.Helper()
	w := request(t, h, http.MethodPost, "/api/v1/bootstrap", body, testAdminToken)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var job bootstrapJobView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &job))
	require.NotEmpty(t, job.JobID)
	assert.Equal(t, bootstrapJobQueued, job.Status)
	assert.Empty(t, job.NodeID)
	return job, w.Body.String()
}

func waitBootstrapJob(t *testing.T, h http.Handler, jobID string) (bootstrapJobView, string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w := request(t, h, http.MethodGet, "/api/v1/bootstrap/"+jobID, "", testAdminToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var job bootstrapJobView
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &job))
		if job.Status == bootstrapJobInstalled || job.Status == bootstrapJobFailed {
			return job, w.Body.String()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("bootstrap job did not reach terminal state")
	return bootstrapJobView{}, ""
}

func startReinstallJob(t *testing.T, h http.Handler, body string) (bootstrapJobView, string) {
	t.Helper()
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/reinstall", body, testAdminToken)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var job bootstrapJobView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &job))
	require.NotEmpty(t, job.JobID)
	assert.Equal(t, bootstrapJobQueued, job.Status)
	assert.Empty(t, job.NodeID)
	return job, w.Body.String()
}

func TestBootstrapRequiresUploadedSignedReleaseBeforeSSH(t *testing.T) {
	installer := bootstrap.NewSSHInstaller("https://panel.test:4200")
	h := NewHandlerWithBootstrap(&fakeStore{}, Config{AdminToken: testAdminToken}, installer)
	w := request(t, h, http.MethodPost, "/api/v1/bootstrap", `{}`, testAdminToken)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"agent_release_required"`)
}

func TestBootstrapDoesNotReturnCredentials(t *testing.T) {
	store := &fakeStore{}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"name":"edge-1","address":"192.0.2.10","username":"root","password":"ssh-secret","allow_firewall_apply":true,"host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	created, postBody := startBootstrapJob(t, h, body)
	assert.NotContains(t, postBody, "ssh-secret")
	installed, pollBody := waitBootstrapJob(t, h, created.JobID)
	require.Equal(t, bootstrapJobInstalled, installed.Status, pollBody)
	assert.Equal(t, "ssh-secret", installer.request.Password)
	assert.NotEmpty(t, installer.request.EnrollmentToken)
	assert.NotContains(t, pollBody, "ssh-secret")
	assert.NotContains(t, pollBody, installer.request.EnrollmentToken)
	assert.Equal(t, testNodeID, installed.NodeID)
	assert.Equal(t, 22, store.createdNode.Metadata["ssh_port"])
	assert.Equal(t, 4200, store.createdNode.Metadata["agent_port"])
	assert.Equal(t, true, store.createdNode.Metadata["firewall_apply_allowed"])
	assert.True(t, installer.request.AllowFirewallApply)
	assert.False(t, installer.request.CredentialRotation)
	audit, err := json.Marshal(store.auditEvents)
	require.NoError(t, err)
	assert.NotContains(t, string(audit), "ssh-secret")
	assert.NotContains(t, string(audit), installer.request.EnrollmentToken)
}

func TestBootstrapPOSTReturnsAcceptedBeforeInstallerCompletes(t *testing.T) {
	installer := &blockingBootstrapInstaller{started: make(chan struct{}, 1), release: make(chan struct{})}
	h := NewHandlerWithBootstrap(&fakeStore{}, Config{AdminToken: testAdminToken}, installer)
	body := `{"name":"edge-1","address":"192.0.2.10","username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testAdminToken)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, r)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		close(installer.release)
		<-done
		t.Fatal("POST /api/v1/bootstrap waited for installer completion")
	}
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var created bootstrapJobView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	select {
	case <-installer.started:
	case <-time.After(time.Second):
		t.Fatal("installer did not start")
	}
	close(installer.release)
	installed, _ := waitBootstrapJob(t, h, created.JobID)
	assert.Equal(t, bootstrapJobInstalled, installed.Status)
}

func TestBootstrapAcceptsPrivateKeyWithoutPersistingOrReturningIt(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "nodeflow-test", []byte("key-passphrase"))
	require.NoError(t, err)
	privateKey := string(pem.EncodeToMemory(block))
	bodyBytes, err := json.Marshal(map[string]any{
		"name":                   "edge-key",
		"address":                "192.0.2.11",
		"username":               "ubuntu",
		"auth_mode":              "private_key",
		"private_key":            privateKey,
		"private_key_passphrase": "key-passphrase",
		"sudo_mode":              "password",
		"sudo_password":          "sudo-secret",
		"host_key_sha256":        "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	require.NoError(t, err)

	store := &fakeStore{}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	created, postBody := startBootstrapJob(t, h, string(bodyBytes))
	installed, pollBody := waitBootstrapJob(t, h, created.JobID)
	require.Equal(t, bootstrapJobInstalled, installed.Status, pollBody)
	assert.Equal(t, bootstrap.AuthModePrivateKey, installer.request.AuthMode)
	assert.Equal(t, bootstrap.SudoModePassword, installer.request.SudoMode)
	assert.Equal(t, privateKey, installer.request.PrivateKey)
	assert.Equal(t, "sudo-secret", installer.request.SudoPassword)
	assert.NotContains(t, postBody, "OPENSSH PRIVATE KEY")
	assert.NotContains(t, pollBody, "OPENSSH PRIVATE KEY")
	assert.NotContains(t, pollBody, "key-passphrase")
	assert.NotContains(t, pollBody, "sudo-secret")
	metadata, err := json.Marshal(store.createdNode.Metadata)
	require.NoError(t, err)
	assert.NotContains(t, string(metadata), "OPENSSH PRIVATE KEY")
	assert.NotContains(t, string(metadata), "sudo-secret")
}

func TestRotateNodeCredentialsKeepsOldTokensWhenNewHeartbeatCannotBeVerified(t *testing.T) {
	store := &fakeStore{credentialUseErr: errors.New("database unavailable")}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/rotate-credentials", body, testAdminToken)
	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Empty(t, store.revokedExceptID)
	assert.Contains(t, w.Body.String(), "credential_rotation_unverified")
}

func TestBootstrapReturnsSafeStageError(t *testing.T) {
	store := &fakeStore{}
	installer := &fakeInstaller{err: &bootstrap.StageError{Stage: "connect"}}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"name":"edge-1","address":"192.0.2.10","username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	created, postBody := startBootstrapJob(t, h, body)
	failed, pollBody := waitBootstrapJob(t, h, created.JobID)
	assert.Equal(t, bootstrapJobFailed, failed.Status)
	assert.Equal(t, "connect", failed.Stage)
	assert.Empty(t, failed.NodeID)
	assert.NotContains(t, postBody, "ssh-secret")
	assert.NotContains(t, pollBody, "ssh-secret")
}

func TestBootstrapDeadlineTakesPrecedenceOverInstallerStage(t *testing.T) {
	store := &fakeStore{}
	api := &API{store: store, bootstrap: &fakeInstaller{err: &bootstrap.StageError{Stage: "install"}}}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	request := &bootstrap.Request{Name: "edge-1", Address: "192.0.2.10", AgentPort: 4200, SSHPort: 22}

	outcome := api.runBootstrapJob(ctx, request, auditActor{})
	assert.Equal(t, "timeout", outcome.Stage)
	assert.Empty(t, outcome.NodeID)
}

func TestReinstallExistingNodePreservesIdentityAndRoutes(t *testing.T) {
	routes := []Route{{ID: testRouteID, NodeID: testNodeID, Name: "api", Enabled: true}}
	store := &fakeStore{
		nodes: []Node{{
			ID: testNodeID, Name: "edge-1", Address: "192.0.2.10",
			Metadata: map[string]any{"ssh_port": float64(2222), "agent_port": float64(4317), "firewall_apply_allowed": true},
		}},
		routes:         routes,
		releases:       []AgentRelease{{ID: testRouteID, Version: "1.0.1", OS: "linux", Arch: "amd64", Sequence: 4}},
		firewallPolicy: NodeFirewallPolicy{NodeID: testNodeID, Mode: "apply"},
	}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","allow_firewall_apply":true,"release_id":"` + testRouteID + `","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	created, postBody := startReinstallJob(t, h, body)
	installed, pollBody := waitBootstrapJob(t, h, created.JobID)

	require.Equal(t, bootstrapJobInstalled, installed.Status, pollBody)
	assert.Equal(t, testNodeID, installed.NodeID)
	assert.True(t, installer.request.Reinstall)
	assert.False(t, installer.request.CredentialRotation)
	assert.Equal(t, testNodeID, installer.request.NodeID)
	assert.Equal(t, "edge-1", installer.request.Name)
	assert.Equal(t, "192.0.2.10", installer.request.Address)
	assert.Equal(t, 2222, installer.request.SSHPort)
	assert.Equal(t, 4317, installer.request.AgentPort)
	assert.True(t, installer.request.AllowFirewallApply)
	assert.Equal(t, "apply", store.firewallPolicy.Mode)
	assert.NotEmpty(t, installer.request.EnrollmentToken)
	assert.Empty(t, store.createdNode.Name, "reinstall must not create a node row")
	assert.Equal(t, routes, store.routes, "reinstall must not mutate routes")
	assert.Equal(t, testRouteID, store.assignedReleaseID)
	assert.Equal(t, testRouteID, store.revokedExceptID)
	assert.Zero(t, installer.rollbackCalls)
	assert.Equal(t, 1, installer.finalizeCalls)
	require.Len(t, store.auditEvents, 1)
	assert.Equal(t, "node.reinstall", store.auditEvents[0].Action)
	assert.Equal(t, testNodeID, store.auditEvents[0].ResourceID)
	assert.NotContains(t, postBody, "ssh-secret")
	assert.NotContains(t, pollBody, "ssh-secret")
	assert.NotContains(t, pollBody, installer.request.EnrollmentToken)
}

func TestReinstallRequestDisablesFirewallApplyAndPersistsObservePolicy(t *testing.T) {
	store := &fakeStore{
		nodes: []Node{{
			ID: testNodeID, Name: "edge-1", Address: "192.0.2.10",
			Metadata: map[string]any{"firewall_apply_allowed": true},
		}},
		firewallPolicy: NodeFirewallPolicy{NodeID: testNodeID, Mode: "off"},
	}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	created, _ := startReinstallJob(t, h, body)
	installed, pollBody := waitBootstrapJob(t, h, created.JobID)

	require.Equal(t, bootstrapJobInstalled, installed.Status, pollBody)
	assert.False(t, installer.request.AllowFirewallApply)
	assert.Equal(t, "observe", store.firewallPolicy.Mode)
}

func TestReinstallQueuedJobRefreshesCurrentNodeWithoutReplacingRequestedFirewallPolicy(t *testing.T) {
	staleNode := Node{
		ID: testNodeID, Name: "stale-name", Address: "192.0.2.10",
		Metadata: map[string]any{"ssh_port": float64(2201), "agent_port": float64(4301)},
	}
	currentNode := Node{
		ID: testNodeID, Name: "current-name", Address: "192.0.2.20",
		Metadata: map[string]any{"ssh_port": float64(2202), "agent_port": float64(4302)},
	}
	var nodeReads atomic.Int32
	var policyReads atomic.Int32
	store := &fakeStore{
		getNodeHook: func(context.Context, string) (Node, error) {
			if nodeReads.Add(1) == 1 {
				return staleNode, nil
			}
			return currentNode, nil
		},
		getFirewallPolicyHook: func(context.Context, string) (NodeFirewallPolicy, error) {
			if policyReads.Add(1) == 1 {
				return NodeFirewallPolicy{NodeID: testNodeID, Mode: "off"}, nil
			}
			return NodeFirewallPolicy{NodeID: testNodeID, Mode: "apply"}, nil
		},
	}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","allow_firewall_apply":true,"host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	created, _ := startReinstallJob(t, h, body)
	installed, pollBody := waitBootstrapJob(t, h, created.JobID)

	require.Equal(t, bootstrapJobInstalled, installed.Status, pollBody)
	assert.GreaterOrEqual(t, nodeReads.Load(), int32(2))
	assert.Zero(t, policyReads.Load())
	assert.Equal(t, "current-name", installer.request.Name)
	assert.Equal(t, "192.0.2.20", installer.request.Address)
	assert.Equal(t, 2202, installer.request.SSHPort)
	assert.Equal(t, 4302, installer.request.AgentPort)
	assert.True(t, installer.request.AllowFirewallApply)
	assert.Equal(t, "apply", store.firewallPolicy.Mode)
}

func TestReinstallAuditUsesBoundedBackgroundContextAfterJobCancellation(t *testing.T) {
	jobContext, cancelJob := context.WithCancel(context.Background())
	t.Cleanup(cancelJob)
	var auditContextErr error
	store := &fakeStore{
		appendAuditHook: func(ctx context.Context, _ AuditEvent) error {
			auditContextErr = ctx.Err()
			return auditContextErr
		},
	}
	installer := &fakeInstaller{finalizeHook: cancelJob}
	api := &API{store: store, bootstrap: installer}
	request := &bootstrap.Request{NodeID: testNodeID, SSHPort: 22, AgentPort: 4200}

	outcome := api.runReinstallJob(jobContext, request, auditActor{Type: "admin_token"}, "192.0.2.50")

	require.ErrorIs(t, jobContext.Err(), context.Canceled)
	assert.Equal(t, "installed", outcome.Stage)
	assert.NoError(t, auditContextErr)
	require.Len(t, store.auditEvents, 1)
	assert.Equal(t, "node.reinstall", store.auditEvents[0].Action)
}

func TestReinstallRejectsUnsupportedAuthenticationBeforeQueueing(t *testing.T) {
	store := &fakeStore{}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","auth_mode":"keyboard_interactive","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/reinstall", body, testAdminToken)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "auth_mode must be password or private_key")
	assert.Empty(t, installer.request.NodeID)
	assert.Empty(t, store.enrollmentHash)
}

func TestReinstallRejectsMalformedHostFingerprintBeforeQueueing(t *testing.T) {
	store := &fakeStore{}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:not-a-valid-fingerprint"}`
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/reinstall", body, testAdminToken)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "valid SHA-256 fingerprint")
	assert.Empty(t, installer.request.NodeID)
	assert.Empty(t, store.enrollmentHash)
}

func TestReinstallPinnedHostMismatchFailsPollingSafely(t *testing.T) {
	store := &fakeStore{}
	installer := &fakeInstaller{err: &bootstrap.StageError{Stage: "connect"}}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	created, postBody := startReinstallJob(t, h, body)
	failed, pollBody := waitBootstrapJob(t, h, created.JobID)

	assert.Equal(t, bootstrapJobFailed, failed.Status)
	assert.Equal(t, "connect", failed.Stage)
	assert.Empty(t, failed.NodeID)
	assert.Equal(t, testRouteID, store.revokedTokenID)
	assert.Zero(t, installer.rollbackCalls, "connect failure occurs before remote state can change")
	assert.NotContains(t, postBody, "ssh-secret")
	assert.NotContains(t, pollBody, "ssh-secret")
	assert.NotContains(t, pollBody, installer.request.EnrollmentToken)
}

func TestReinstallInstallerFailureRollsBackAndRevokesCandidate(t *testing.T) {
	store := &fakeStore{}
	installer := &fakeInstaller{err: &bootstrap.StageError{Stage: "install"}}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	created, _ := startReinstallJob(t, h, body)
	failed, pollBody := waitBootstrapJob(t, h, created.JobID)

	require.Equal(t, bootstrapJobFailed, failed.Status, pollBody)
	assert.Equal(t, "install", failed.Stage)
	assert.Equal(t, 1, installer.rollbackCalls)
	assert.Zero(t, installer.finalizeCalls)
	assert.Equal(t, testRouteID, store.revokedTokenID)
	assert.Empty(t, store.revokedExceptID, "old credentials must remain active")
	assert.Empty(t, store.auditEvents, "failed reinstall must not emit success audit")
}

func TestReinstallHeartbeatFailureRollsBackAndKeepsOldCredentials(t *testing.T) {
	store := &fakeStore{credentialUseErr: errors.New("database unavailable")}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	created, _ := startReinstallJob(t, h, body)
	failed, pollBody := waitBootstrapJob(t, h, created.JobID)

	require.Equal(t, bootstrapJobFailed, failed.Status, pollBody)
	assert.Equal(t, "verify_credential", failed.Stage)
	assert.Equal(t, 1, installer.rollbackCalls)
	assert.Equal(t, testRouteID, store.revokedTokenID)
	assert.Empty(t, store.revokedExceptID)
	assert.Zero(t, installer.finalizeCalls)
	assert.Empty(t, store.auditEvents)
}

func TestReinstallRevokeFailureRollsBackBeforeRevokingCandidate(t *testing.T) {
	store := &fakeStore{revokeOtherFails: credentialStoreRetryAttempts}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	created, _ := startReinstallJob(t, h, body)
	failed, pollBody := waitBootstrapJob(t, h, created.JobID)

	require.Equal(t, bootstrapJobFailed, failed.Status, pollBody)
	assert.Equal(t, "revoke_credentials", failed.Stage)
	assert.Equal(t, credentialStoreRetryAttempts, store.revokeOtherCalls)
	assert.Equal(t, 1, installer.rollbackCalls)
	assert.Equal(t, testRouteID, store.revokedTokenID)
	assert.Zero(t, installer.finalizeCalls)
	assert.Empty(t, store.auditEvents)
}

func TestReinstallRequiresRollbackCapableInstaller(t *testing.T) {
	installer := &blockingBootstrapInstaller{started: make(chan struct{}, 1), release: make(chan struct{})}
	h := NewHandlerWithBootstrap(&fakeStore{}, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/reinstall", body, testAdminToken)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "reinstall_rollback_unavailable")
}

func TestReinstallAcceptsPrivateKeyAndNonRootSudo(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(private, "nodeflow-reinstall")
	require.NoError(t, err)
	privateKey := string(pem.EncodeToMemory(block))
	bodyBytes, err := json.Marshal(map[string]any{
		"username": "ubuntu", "auth_mode": "private_key", "private_key": privateKey,
		"sudo_mode": "password", "sudo_password": "sudo-secret",
		"host_key_sha256": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	require.NoError(t, err)
	store := &fakeStore{}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	created, postBody := startReinstallJob(t, h, string(bodyBytes))
	installed, pollBody := waitBootstrapJob(t, h, created.JobID)

	require.Equal(t, bootstrapJobInstalled, installed.Status, pollBody)
	assert.Equal(t, bootstrap.AuthModePrivateKey, installer.request.AuthMode)
	assert.Equal(t, bootstrap.SudoModePassword, installer.request.SudoMode)
	assert.Equal(t, privateKey, installer.request.PrivateKey)
	assert.Equal(t, "sudo-secret", installer.request.SudoPassword)
	assert.NotContains(t, postBody, "OPENSSH PRIVATE KEY")
	assert.NotContains(t, pollBody, "OPENSSH PRIVATE KEY")
	assert.NotContains(t, pollBody, "sudo-secret")
}

func TestRotateNodeCredentialsReinstallsAndRevokesSupersededTokens(t *testing.T) {
	store := &fakeStore{
		nodes:          []Node{{ID: testNodeID, Name: "edge-1", Address: "192.0.2.10", Metadata: map[string]any{"agent_port": float64(4317), "firewall_apply_allowed": false}}},
		firewallPolicy: NodeFirewallPolicy{NodeID: testNodeID, Mode: "apply"},
	}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"ssh_port":22,"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/rotate-credentials", body, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, testNodeID, installer.request.NodeID)
	assert.Equal(t, 4317, installer.request.AgentPort)
	assert.True(t, installer.request.AllowFirewallApply)
	assert.True(t, installer.request.CredentialRotation)
	assert.NotEmpty(t, installer.request.EnrollmentToken)
	assert.Equal(t, testRouteID, store.revokedExceptID)
	assert.Equal(t, 1, installer.finalizeCalls)
	assert.NotContains(t, w.Body.String(), installer.request.EnrollmentToken)
}

func TestRotateNodeCredentialsRetriesTransientSupersededRevoke(t *testing.T) {
	store := &fakeStore{revokeOtherFails: 2}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/rotate-credentials", body, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 3, store.revokeOtherCalls)
	assert.Equal(t, testRouteID, store.revokedExceptID)
	assert.Zero(t, installer.rollbackCalls)
	assert.Equal(t, 1, installer.finalizeCalls)
}

func TestRotateNodeCredentialsRollsBackWhenSupersededRevokeKeepsFailing(t *testing.T) {
	store := &fakeStore{revokeOtherFails: credentialStoreRetryAttempts}
	installer := &fakeInstaller{}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/rotate-credentials", body, testAdminToken)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "credential_rotation_rolled_back")
	assert.Equal(t, credentialStoreRetryAttempts, store.revokeOtherCalls)
	assert.Equal(t, 1, installer.rollbackCalls)
	assert.Zero(t, installer.finalizeCalls)
	assert.Equal(t, testRouteID, store.revokedTokenID)
}

func TestRotateNodeCredentialsRevokesNewTokenOnInstallFailure(t *testing.T) {
	store := &fakeStore{}
	installer := &fakeInstaller{err: &bootstrap.StageError{Stage: "activate"}}
	h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
	body := `{"username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/rotate-credentials", body, testAdminToken)
	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Equal(t, testRouteID, store.revokedTokenID)
	assert.Contains(t, w.Body.String(), "activate")
}

func TestNodeAndRouteOrderEndpoints(t *testing.T) {
	h := handler(&fakeStore{nodes: []Node{{ID: testNodeID}}, routes: []Route{{ID: testRouteID, NodeID: testNodeID}}})
	w := request(t, h, http.MethodPut, "/api/v1/nodes/order", `{"node_ids":["`+testNodeID+`"]}`, testAdminToken)
	assert.Equal(t, http.StatusOK, w.Code)
	w = request(t, h, http.MethodPut, "/api/v1/nodes/"+testNodeID+"/routes/order", `{"route_ids":["`+testRouteID+`"]}`, testAdminToken)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHAProxyControlEndpoint(t *testing.T) {
	h := handler(&fakeStore{})
	w := request(t, h, http.MethodGet, "/api/v1/nodes/"+testNodeID+"/haproxy", "", testAdminToken)
	assert.Equal(t, http.StatusOK, w.Code)
	w = request(t, h, http.MethodPut, "/api/v1/nodes/"+testNodeID+"/haproxy", `{"enabled":false,"expected_generation":0}`, testAdminToken)
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), `"desired_enabled":false`)
}

func TestHAProxyRestartEndpoint(t *testing.T) {
	store := &fakeStore{}
	h := handler(store)
	path := "/api/v1/nodes/" + testNodeID + "/haproxy/restart"
	w := request(t, h, http.MethodPost, path, `{"expected_generation":7}`, testAdminToken)
	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, testNodeID, store.restartNodeID)
	assert.Equal(t, int64(7), store.restartGeneration)
	assert.Contains(t, w.Body.String(), `"restart_generation":1`)
	require.Len(t, store.auditEvents, 1)
	assert.Equal(t, "haproxy.service.restart", store.auditEvents[0].Action)

	w = request(t, h, http.MethodPost, path, `{"expected_generation":-1}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = request(t, h, http.MethodGet, path, "", testAdminToken)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHAProxyRestartReportsDisabledAndUnsupported(t *testing.T) {
	path := "/api/v1/nodes/" + testNodeID + "/haproxy/restart"
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "disabled", err: ErrHAProxyDisabled, code: "haproxy_disabled"},
		{name: "unsupported", err: ErrControlUnsupported, code: "haproxy_control_unsupported"},
		{name: "stale", err: ErrControlGenerationConflict, code: "stale_haproxy_control"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{restartErr: test.err}
			w := request(t, handler(store), http.MethodPost, path, `{"expected_generation":0}`, testAdminToken)
			assert.Equal(t, http.StatusConflict, w.Code)
			assert.Contains(t, w.Body.String(), test.code)
		})
	}
}

func TestValidateHAProxyServiceReportAcceptsRestartAcknowledgement(t *testing.T) {
	control, restart := int64(3), int64(2)
	assert.NoError(t, validateHAProxyServiceReport(Heartbeat{
		HAProxyControlGeneration: &control,
		HAProxyRestartGeneration: &restart,
		HAProxyServiceState:      "active",
	}))
	negative := int64(-1)
	assert.ErrorIs(t, validateHAProxyServiceReport(Heartbeat{
		HAProxyControlGeneration: &control,
		HAProxyRestartGeneration: &negative,
		HAProxyServiceState:      "active",
	}), ErrInvalidObservedConfig)
}

func jsonDecode(body string, dst any) error {
	return json.NewDecoder(strings.NewReader(body)).Decode(dst)
}
