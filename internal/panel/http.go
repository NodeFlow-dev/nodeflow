package panel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nodeflow/nodeflow/internal/bootstrap"
)

type API struct {
	store                  Store
	adminToken             string
	bootstrap              bootstrap.Installer
	bootstrapJobs          *bootstrapJobStore
	releases               *ReleaseService
	sessions               *sessionStore
	publicOrigin           string
	publicURL              string
	panelVersion           string
	webPort                int
	agentPort              int
	metricsEnabled         bool
	metricsAllowCIDRs      int
	secureCookies          bool
	requireAgentMTLS       bool
	credentialIssuer       bootstrap.CSRIdentityIssuer
	credentialRenewalLocks sync.Map
	dnsResolve             *dnsResolveService
}

type auditActor struct {
	Type string
	ID   string
}

type auditActorContextKey struct{}

type credentialChangeLock struct {
	held chan struct{}
}

const (
	sessionCookieName       = "nodeflow_session"
	sessionAbsoluteLifetime = 24 * time.Hour
	nodeCredentialTTL       = 825 * 24 * time.Hour
	// A freshly installed Agent collects its first metrics before the initial
	// heartbeat. Twenty seconds proved too short on a clean Ubuntu node and
	// caused a healthy reinstall to be rolled back.
	reinstallVerifyTimeout = 60 * time.Second
)

type browserSessionState struct {
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	Sequence   uint64
}

type sessionStore struct {
	mu           sync.Mutex
	sessions     map[string]browserSessionState
	inactivity   time.Duration
	maxSessions  int
	nextSequence uint64
	now          func() time.Time
}

func NewHandler(store Store, cfg Config) http.Handler {
	return NewHandlerWithBootstrap(store, cfg, nil)
}

func NewHandlerWithBootstrap(store Store, cfg Config, installer bootstrap.Installer) http.Handler {
	return NewHandlerWithServices(store, cfg, installer, nil)
}

func NewHandlerWithServices(store Store, cfg Config, installer bootstrap.Installer, releases *ReleaseService) http.Handler {
	return NewHandlerWithServicesAndIssuer(store, cfg, installer, releases, nil)
}

func NewHandlerWithServicesAndIssuer(store Store, cfg Config, installer bootstrap.Installer, releases *ReleaseService, issuer bootstrap.CSRIdentityIssuer) http.Handler {
	settings := defaultPanelSettings()
	settingsCtx, settingsCancel := context.WithTimeout(context.Background(), 2*time.Second)
	storedSettings, settingsErr := store.GetPanelSettings(settingsCtx)
	settingsCancel()
	if settingsErr == nil && validatePanelSettings(storedSettings) == nil {
		settings = storedSettings
	} else {
		settings = strictPanelSettings()
		if settingsErr != nil {
			slog.Warn("load Panel settings failed; using strict session defaults", "error", settingsErr)
		} else {
			slog.Warn("stored Panel settings are invalid; using strict session defaults")
		}
	}
	a := &API{
		store: store, adminToken: cfg.AdminToken, bootstrap: installer,
		bootstrapJobs: newBootstrapJobStore(defaultBootstrapJobLimit, defaultBootstrapJobConcurrency, defaultBootstrapJobTTL, defaultBootstrapRunTimeout),
		releases:      releases, sessions: newSessionStore(settings), requireAgentMTLS: cfg.RequireAgentMTLS,
		credentialIssuer:  issuer,
		publicURL:         cfg.PublicURL,
		panelVersion:      strings.TrimSpace(cfg.PanelVersion),
		webPort:           firstConfiguredPort(configuredURLPort(cfg.PublicURL), configuredPort(cfg.ListenAddr)),
		agentPort:         firstConfiguredPort(configuredURLPort(cfg.AgentPublicURL), configuredPort(cfg.AgentTLSListenAddr)),
		metricsEnabled:    cfg.MetricsEnabled,
		metricsAllowCIDRs: len(cfg.MetricsAllowCIDRs),
		dnsResolve:        newDNSResolveService(cfg.dnsResolver),
	}
	if a.panelVersion == "" {
		a.panelVersion = "dev"
	}
	if u, err := url.Parse(cfg.PublicURL); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		a.publicOrigin = u.Scheme + "://" + u.Host
		a.secureCookies = u.Scheme == "https"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.HandleFunc("GET /auth/session", a.session)
	mux.HandleFunc("POST /auth/activity", a.activity)
	mux.Handle("/api/v1/", a.admin(a.auditMutations(http.HandlerFunc(a.api))))
	mux.HandleFunc("POST /agent/v1/heartbeat", a.heartbeat)
	mux.HandleFunc("POST /agent/v1/config-report", a.configReport)
	mux.HandleFunc("GET /agent/v1/updates/{sequence}/artifact", a.agentUpdateArtifact)
	mux.HandleFunc("POST /agent/v1/credential-renewals", a.credentialRenewal)
	mux.HandleFunc("POST /agent/v1/credential-renewals/{renewal_id}/confirm", a.confirmCredentialRenewal)
	// F5: Prometheus /metrics endpoint — disabled unless PANEL_METRICS_ENABLED=true.
	if cfg.MetricsEnabled {
		metricsHandler := metricsAuthMiddleware(cfg.MetricsBearerToken, cfg.MetricsAllowCIDRs, func(w http.ResponseWriter, r *http.Request) {
			snap, err := CollectMetrics(r.Context(), store)
			if err != nil {
				http.Error(w, "metrics collection failed", http.StatusInternalServerError)
				return
			}
			RenderPrometheusMetrics(w, snap)
		})
		mux.HandleFunc("GET /metrics", metricsHandler)
	}
	mux.Handle("/", embeddedWebHandler())
	return securityHeaders(limitBody(gzipUIResponses(mux)))
}

type loginInput struct {
	Token string `json:"token"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	if !a.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "same-origin request required")
		return
	}
	var in loginInput
	if !decode(w, r, &in) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.Token), []byte(a.adminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := a.sessions.put(token)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", Expires: expires,
		MaxAge: int(sessionAbsoluteLifetime.Seconds()), HttpOnly: true, Secure: a.secureCookies || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "expires_at": expires})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if !a.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "same-origin request required")
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		a.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secureCookies || r.TLS != nil, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) session(w http.ResponseWriter, r *http.Request) {
	_, ok := a.browserSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid browser session required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (a *API) activity(w http.ResponseWriter, r *http.Request) {
	if !a.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "same-origin request required")
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" || !a.sessions.touch(cookie.Value) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid browser session required")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func newSessionStore(settings PanelSettings) *sessionStore {
	if validatePanelSettings(settings) != nil {
		settings = strictPanelSettings()
	}
	return &sessionStore{
		sessions:    make(map[string]browserSessionState),
		inactivity:  time.Duration(settings.InactivityTimeoutMinutes) * time.Minute,
		maxSessions: settings.MaxSessions,
		now:         time.Now,
	}
}

func (s *sessionStore) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *sessionStore) put(token string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.currentTime()
	s.removeExpiredLocked(now)
	s.nextSequence++
	expires := now.Add(sessionAbsoluteLifetime)
	s.sessions[token] = browserSessionState{CreatedAt: now, LastSeenAt: now, ExpiresAt: expires, Sequence: s.nextSequence}
	s.enforceLimitLocked()
	return expires
}

func (s *sessionStore) check(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.currentTime()
	session, ok := s.sessions[token]
	if !ok || s.expiredLocked(session, now) {
		delete(s.sessions, token)
		return false
	}
	return true
}

// touch first evaluates the old inactivity deadline and only then records user
// activity. An expired session can therefore never be revived by this call.
func (s *sessionStore) touch(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.currentTime()
	session, ok := s.sessions[token]
	if !ok || s.expiredLocked(session, now) {
		delete(s.sessions, token)
		return false
	}
	session.LastSeenAt = now
	s.sessions[token] = session
	return true
}

func (s *sessionStore) configure(settings PanelSettings) {
	if validatePanelSettings(settings) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Expiry must be evaluated using the policy that governed the sessions up
	// to this instant. Applying a more permissive policy must not resurrect an
	// already expired session.
	now := s.currentTime()
	s.removeExpiredLocked(now)
	s.inactivity = time.Duration(settings.InactivityTimeoutMinutes) * time.Minute
	s.maxSessions = settings.MaxSessions
	// A stricter replacement policy takes effect immediately as well.
	s.removeExpiredLocked(now)
	s.enforceLimitLocked()
}

func (s *sessionStore) expiredLocked(session browserSessionState, now time.Time) bool {
	return !session.ExpiresAt.After(now) || !session.LastSeenAt.Add(s.inactivity).After(now)
}

func (s *sessionStore) removeExpiredLocked(now time.Time) {
	for token, session := range s.sessions {
		if s.expiredLocked(session, now) {
			delete(s.sessions, token)
		}
	}
}

func (s *sessionStore) enforceLimitLocked() {
	for len(s.sessions) > s.maxSessions {
		oldestToken := ""
		var oldest browserSessionState
		for token, session := range s.sessions {
			if oldestToken == "" || session.LastSeenAt.Before(oldest.LastSeenAt) ||
				(session.LastSeenAt.Equal(oldest.LastSeenAt) && session.CreatedAt.Before(oldest.CreatedAt)) ||
				(session.LastSeenAt.Equal(oldest.LastSeenAt) && session.CreatedAt.Equal(oldest.CreatedAt) && session.Sequence < oldest.Sequence) ||
				(session.LastSeenAt.Equal(oldest.LastSeenAt) && session.CreatedAt.Equal(oldest.CreatedAt) && session.Sequence == oldest.Sequence && token < oldestToken) {
				oldestToken, oldest = token, session
			}
		}
		delete(s.sessions, oldestToken)
	}
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextTimeout(r, 2*time.Second)
	defer cancel()
	if err := a.store.Health(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": a.panelVersion})
}

func (a *API) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r.Header.Get("Authorization"))
		if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(a.adminToken)) == 1 {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auditActorContextKey{}, auditActor{Type: "admin_token", ID: "primary"})))
			return
		}
		sessionID, ok := a.browserSession(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		if mutatingMethod(r.Method) && !a.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "invalid_origin", "same-origin request required")
			return
		}
		sum := sha256.Sum256([]byte(sessionID))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auditActorContextKey{}, auditActor{Type: "browser_session", ID: hex.EncodeToString(sum[:6])})))
	})
}

type auditStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditStatusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditStatusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (a *API) auditMutations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trimmedPath := strings.TrimRight(r.URL.Path, "/")
		if !mutatingMethod(r.Method) || trimmedPath == "/api/v1/bootstrap" ||
			(r.Method == http.MethodPost && strings.HasSuffix(trimmedPath, "/reinstall")) {
			next.ServeHTTP(w, r)
			return
		}
		wrapped := &auditStatusWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
		status := wrapped.status
		if status == 0 {
			status = http.StatusOK
		}
		if status < 200 || status >= 400 {
			return
		}
		event := auditEventForRequest(r, status)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := a.store.AppendAudit(ctx, event); err != nil {
			slog.Error("append audit event failed", "action", event.Action, "error", err)
		}
	})
}

func auditEventForRequest(r *http.Request, status int) AuditEvent {
	actor, _ := r.Context().Value(auditActorContextKey{}).(auditActor)
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/"), "/")
	event := AuditEvent{ActorType: actor.Type, ActorID: actor.ID, Action: "api.mutate", ResourceType: "api", Details: map[string]any{"method": r.Method, "status": status}}
	if event.ActorType == "" {
		event.ActorType = "unknown_admin"
	}
	if len(parts) > 1 && parts[0] == "nodes" && validID(parts[1]) {
		nodeID := parts[1]
		event.ResourceType, event.ResourceID = "node", nodeID
		event.Details["node_id"] = nodeID
		switch {
		case len(parts) == 2 && r.Method == http.MethodPut:
			event.Action = "node.update"
		case len(parts) == 2 && r.Method == http.MethodDelete:
			event.Action = "node.delete"
		case len(parts) == 4 && parts[2] == "routes" && parts[3] == "order":
			event.Action, event.ResourceType = "route.order", "route"
		case len(parts) >= 3 && parts[2] == "routes":
			event.Action, event.ResourceType = "route.change", "route"
			if len(parts) == 4 && validID(parts[3]) {
				event.ResourceID = parts[3]
			}
		case len(parts) == 3 && parts[2] == "firewall":
			event.Action = "firewall.policy"
		case len(parts) == 3 && parts[2] == "haproxy":
			event.Action = "haproxy.service.control"
		case len(parts) == 4 && parts[2] == "haproxy" && parts[3] == "restart":
			event.Action = "haproxy.service.restart"
		case len(parts) == 3 && parts[2] == "agent-update":
			event.Action = "agent.update.assign"
		case len(parts) == 4 && parts[2] == "agent-update" && parts[3] == "rollback":
			event.Action = "agent.update.rollback"
		case len(parts) >= 3 && parts[2] == "config-revisions":
			event.Action, event.ResourceType = "revision.create", "config_revision"
		case len(parts) == 3 && parts[2] == "desired-revision":
			event.Action, event.ResourceType = "revision.assign", "config_revision"
		case len(parts) == 3 && parts[2] == "enrollment-tokens":
			event.Action, event.ResourceType = "credential.issue", "node_credential"
		case len(parts) == 3 && parts[2] == "rotate-credentials":
			event.Action, event.ResourceType = "credential.rotate", "node_credential"
		case len(parts) == 3 && parts[2] == "reinstall":
			event.Action = "node.reinstall"
		}
	} else if len(parts) == 1 && parts[0] == "nodes" && r.Method == http.MethodPost {
		event.Action, event.ResourceType = "node.create", "node"
	} else if len(parts) == 2 && parts[0] == "nodes" && parts[1] == "order" {
		event.Action, event.ResourceType = "node.order", "node"
	} else if len(parts) == 1 && parts[0] == "agent-releases" && r.Method == http.MethodPost {
		event.Action, event.ResourceType = "agent.release.create", "agent_release"
	} else if len(parts) == 2 && parts[0] == "agent-releases" && r.Method == http.MethodDelete {
		event.Action, event.ResourceType, event.ResourceID = "agent.release.delete", "agent_release", parts[1]
	} else if len(parts) == 1 && parts[0] == "settings" && r.Method == http.MethodPut {
		event.Action, event.ResourceType, event.ResourceID = "settings.update", "panel_settings", "primary"
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && net.ParseIP(host) != nil {
		event.SourceIP = host
	}
	return event
}

func (a *API) browserSession(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" || !a.sessions.check(cookie.Value) {
		return "", false
	}
	return cookie.Value, true
}

func mutatingMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func (a *API) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	expected := a.publicOrigin
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return strings.EqualFold(u.Scheme+"://"+u.Host, expected)
}

func (a *API) api(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 1 && parts[0] == "overview" {
		a.dashboardOverview(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "settings" {
		a.panelSettings(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "bootstrap" {
		a.bootstrapNode(w, r)
		return
	}
	if len(parts) == 2 && parts[0] == "bootstrap" && parts[1] == "host-key" {
		a.hostKeyScan(w, r)
		return
	}
	if len(parts) == 2 && parts[0] == "bootstrap" {
		a.bootstrapJob(w, r, parts[1])
		return
	}
	if len(parts) == 2 && parts[0] == "dns" && parts[1] == "resolve" {
		a.dnsResolve.ServeHTTP(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "nodes" {
		a.nodes(w, r)
		return
	}
	if len(parts) == 2 && parts[0] == "nodes" && parts[1] == "order" {
		a.nodeOrder(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "agent-releases" {
		a.agentReleases(w, r)
		return
	}
	if len(parts) == 2 && parts[0] == "agent-releases" && parts[1] == "signing-key" {
		a.agentReleaseSigningKey(w, r)
		return
	}
	if len(parts) == 2 && parts[0] == "agent-releases" {
		if !validID(parts[1]) {
			writeError(w, http.StatusBadRequest, "invalid_id", "invalid Agent release id")
			return
		}
		a.agentRelease(w, r, parts[1])
		return
	}
	if len(parts) >= 2 && parts[0] == "nodes" {
		nodeID := parts[1]
		if !validID(nodeID) {
			writeError(w, 400, "invalid_id", "invalid node id")
			return
		}
		if len(parts) == 2 {
			a.node(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "routes" {
			a.routes(w, r, nodeID)
			return
		}
		if len(parts) == 4 && parts[2] == "routes" && parts[3] == "order" {
			a.routeOrder(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "operational" {
			a.nodeOperational(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "audit" {
			a.nodeAudit(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "traffic" {
			a.nodeTraffic(w, r, nodeID)
			return
		}
		if len(parts) == 4 && parts[2] == "traffic" && parts[3] == "history" {
			a.nodeTrafficHistory(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "firewall" {
			a.nodeFirewall(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "haproxy" {
			a.nodeHAProxy(w, r, nodeID)
			return
		}
		if len(parts) == 4 && parts[2] == "haproxy" && parts[3] == "restart" {
			a.nodeHAProxyRestart(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "agent-update" {
			a.nodeAgentUpdate(w, r, nodeID)
			return
		}
		if len(parts) == 4 && parts[2] == "agent-update" && parts[3] == "rollback" {
			a.nodeAgentUpdateRollback(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "render-config" {
			a.renderConfig(w, r, nodeID)
			return
		}
		if len(parts) == 4 && parts[2] == "routes" {
			if !validID(parts[3]) {
				writeError(w, 400, "invalid_id", "invalid route id")
				return
			}
			a.route(w, r, nodeID, parts[3])
			return
		}
		if len(parts) == 6 && parts[2] == "routes" && parts[4] == "traffic" && parts[5] == "history" {
			if !validID(parts[3]) {
				writeError(w, 400, "invalid_id", "invalid route id")
				return
			}
			a.routeTrafficHistory(w, r, nodeID, parts[3])
			return
		}
		if len(parts) == 3 && parts[2] == "enrollment-tokens" {
			a.enrollment(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "rotate-credentials" {
			a.rotateNodeCredentials(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "reinstall" {
			a.reinstallNode(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "config-revisions" {
			a.configRevisions(w, r, nodeID)
			return
		}
		if len(parts) == 4 && parts[2] == "config-revisions" && parts[3] == "from-routes" {
			a.configRevisionFromRoutes(w, r, nodeID)
			return
		}
		if len(parts) == 4 && parts[2] == "config-revisions" {
			revision, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil || revision < 1 {
				writeError(w, 400, "invalid_revision", "revision must be a positive integer")
				return
			}
			a.configRevision(w, r, nodeID, revision)
			return
		}
		if len(parts) == 3 && parts[2] == "config-state" {
			a.configState(w, r, nodeID)
			return
		}
		if len(parts) == 3 && parts[2] == "desired-revision" {
			a.desiredRevision(w, r, nodeID)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "resource not found")
}

type panelSettingsInput struct {
	Theme                    string `json:"theme"`
	Accent                   string `json:"accent"`
	InactivityTimeoutMinutes int    `json:"session_timeout_minutes"`
	MaxSessions              int    `json:"max_sessions"`
	AuditRetentionDays       int    `json:"audit_retention_days"`
}

type panelSettingsResponse struct {
	PanelSettings
	PublicURL string `json:"public_url"`
	WebPort   int    `json:"web_port"`
	AgentPort int    `json:"agent_port"`
	// Read-only runtime view of the Prometheus endpoint; the token is never exposed.
	MetricsEnabled    bool `json:"metrics_enabled"`
	MetricsAllowCIDRs int  `json:"metrics_allow_cidrs"`
}

func (a *API) panelSettings(w http.ResponseWriter, r *http.Request) {
	var settings PanelSettings
	var err error
	switch r.Method {
	case http.MethodGet:
		settings, err = a.store.GetPanelSettings(r.Context())
	case http.MethodPut:
		var input panelSettingsInput
		if !decode(w, r, &input) {
			return
		}
		settings = PanelSettings{
			Theme:                    input.Theme,
			Accent:                   input.Accent,
			InactivityTimeoutMinutes: input.InactivityTimeoutMinutes,
			MaxSessions:              input.MaxSessions,
			AuditRetentionDays:       input.AuditRetentionDays,
		}
		if validationErr := validatePanelSettings(settings); validationErr != nil {
			writeError(w, http.StatusBadRequest, "validation_error", validationErr.Error())
			return
		}
		settings, err = a.store.UpdatePanelSettings(r.Context(), settings)
	default:
		methodNotAllowed(w, "GET, PUT")
		return
	}
	if err == nil {
		a.sessions.configure(settings)
	}
	response := panelSettingsResponse{
		PanelSettings:     settings,
		PublicURL:         a.publicURL,
		WebPort:           a.webPort,
		AgentPort:         a.agentPort,
		MetricsEnabled:    a.metricsEnabled,
		MetricsAllowCIDRs: a.metricsAllowCIDRs,
	}
	respondStore(w, response, err, http.StatusOK)
}

func (a *API) nodeOperational(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	v, err := a.store.GetNodeOperationalDetail(r.Context(), nodeID)
	respondStore(w, v, err, http.StatusOK)
}

func (a *API) nodeAudit(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "validation_error", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	entries, err := a.store.ListAudit(r.Context(), nodeID, limit)
	respondStore(w, entries, err, http.StatusOK)
}

func (a *API) nodeTraffic(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	values, supplied := r.URL.Query()["month"]
	if supplied && len(values) != 1 {
		writeError(w, http.StatusBadRequest, "validation_error", "month must use YYYY-MM")
		return
	}
	value := ""
	if supplied {
		value = values[0]
	}
	month, err := parseTrafficMonth(value, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	v, err := a.store.GetTraffic(r.Context(), nodeID, month)
	respondStore(w, v, err, http.StatusOK)
}

type firewallPolicyInput struct {
	Mode string `json:"mode"`
}

func (a *API) nodeFirewall(w http.ResponseWriter, r *http.Request, nodeID string) {
	switch r.Method {
	case http.MethodGet:
		v, err := a.store.GetFirewallPolicy(r.Context(), nodeID)
		respondStore(w, v, err, http.StatusOK)
	case http.MethodPut:
		var in firewallPolicyInput
		if !decode(w, r, &in) {
			return
		}
		mode, ok := normalizeFirewallMode(strings.TrimSpace(in.Mode))
		if !ok {
			writeError(w, http.StatusBadRequest, "validation_error", "mode must be off, observe or apply")
			return
		}
		v, err := a.store.UpdateFirewallPolicy(r.Context(), nodeID, mode)
		respondStore(w, v, err, http.StatusOK)
	default:
		methodNotAllowed(w, "GET, PUT")
	}
}

type agentReleaseAssignmentInput struct {
	ReleaseID               string `json:"release_id"`
	ExpectedActualSequence  *int64 `json:"expected_actual_sequence"`
	ExpectedDesiredSequence *int64 `json:"expected_desired_sequence"`
}

type agentReleaseRollbackInput struct {
	TargetReleaseID         string `json:"target_release_id"`
	ExpectedActualSequence  int64  `json:"expected_actual_sequence"`
	ExpectedDesiredSequence *int64 `json:"expected_desired_sequence"`
}

func (a *API) nodeAgentUpdate(w http.ResponseWriter, r *http.Request, nodeID string) {
	switch r.Method {
	case http.MethodGet:
		state, err := a.store.GetAgentUpdateState(r.Context(), nodeID)
		respondStore(w, state, err, http.StatusOK)
	case http.MethodPut:
		var input agentReleaseAssignmentInput
		if !decode(w, r, &input) {
			return
		}
		if !validID(input.ReleaseID) {
			writeError(w, http.StatusBadRequest, "validation_error", "release_id must be a UUID")
			return
		}
		if input.ExpectedActualSequence == nil || *input.ExpectedActualSequence < 0 {
			writeError(w, http.StatusBadRequest, "validation_error", "expected_actual_sequence must be a nonnegative integer")
			return
		}
		if input.ExpectedDesiredSequence == nil || *input.ExpectedDesiredSequence < 0 {
			writeError(w, http.StatusBadRequest, "validation_error", "expected_desired_sequence must be a nonnegative integer")
			return
		}
		state, err := a.store.AssignAgentRelease(r.Context(), nodeID, input.ReleaseID, *input.ExpectedActualSequence, *input.ExpectedDesiredSequence)
		if errors.Is(err, ErrAgentUpdateStateChanged) {
			writeError(w, http.StatusConflict, "update_state_changed", err.Error())
			return
		}
		if errors.Is(err, ErrReleaseNotNewer) {
			writeError(w, http.StatusConflict, "release_not_newer", err.Error())
			return
		}
		if errors.Is(err, ErrReleasePlatform) {
			writeError(w, http.StatusConflict, "platform_mismatch", err.Error())
			return
		}
		if errors.Is(err, ErrReleasePlatformUnknown) {
			writeError(w, http.StatusConflict, "node_platform_unknown", err.Error())
			return
		}
		respondStore(w, state, err, http.StatusOK)
	default:
		methodNotAllowed(w, "GET, PUT")
	}
}

func (a *API) nodeAgentUpdateRollback(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if a.releases == nil {
		writeError(w, http.StatusServiceUnavailable, "release_service_unavailable", "Agent release signing is not configured")
		return
	}
	var input agentReleaseRollbackInput
	if !decode(w, r, &input) {
		return
	}
	if !validID(input.TargetReleaseID) {
		writeError(w, http.StatusBadRequest, "validation_error", "target_release_id must be a UUID")
		return
	}
	if input.ExpectedActualSequence < 1 {
		writeError(w, http.StatusBadRequest, "validation_error", "expected_actual_sequence must be a positive integer")
		return
	}
	if input.ExpectedDesiredSequence == nil || *input.ExpectedDesiredSequence < 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "expected_desired_sequence must be a nonnegative integer")
		return
	}

	state, err := a.store.GetAgentUpdateState(r.Context(), nodeID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "update_state_unavailable", "Agent update state is unavailable")
		return
	}
	if state.ActualSequence != input.ExpectedActualSequence {
		writeError(w, http.StatusConflict, "actual_sequence_changed", "installed Agent release changed; reload state and retry")
		return
	}
	desiredSequence := int64(0)
	if state.DesiredRelease != nil {
		desiredSequence = state.DesiredRelease.Sequence
	}
	if desiredSequence != *input.ExpectedDesiredSequence {
		writeError(w, http.StatusConflict, "update_state_changed", "Agent update state changed; reload and retry")
		return
	}
	if state.State != "installed" && state.State != "idle" {
		writeError(w, http.StatusConflict, "update_in_progress", "Agent update must be installed or idle before rollback")
		return
	}

	releases, err := a.store.ListAgentReleases(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "release_store_unavailable", "Agent releases are unavailable")
		return
	}
	var target, current *AgentRelease
	for index := range releases {
		release := &releases[index]
		if release.ID == input.TargetReleaseID {
			target = release
		}
		if release.Sequence == state.ActualSequence {
			current = release
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "not_found", "target Agent release not found")
		return
	}
	if current == nil {
		writeError(w, http.StatusConflict, "installed_release_unknown", "installed Agent release is not available for verification")
		return
	}
	if target.Sequence >= current.Sequence {
		writeError(w, http.StatusConflict, "invalid_rollback_target", "target Agent release must be older than the installed release")
		return
	}
	if target.OS != current.OS || target.Arch != current.Arch {
		writeError(w, http.StatusConflict, "platform_mismatch", "target Agent release platform does not match the installed release")
		return
	}

	clone, err := a.releases.CloneVerified(r.Context(), *target)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrReleaseArtifactIntegrity) {
		writeError(w, http.StatusConflict, "release_artifact_invalid", "target Agent release artifact is unavailable or failed integrity verification")
		return
	}
	var inputError *ReleaseInputError
	if errors.As(err, &inputError) {
		writeError(w, http.StatusBadRequest, "invalid_release", "target Agent release metadata is invalid")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "release_clone_failed", "could not create signed rollback release")
		return
	}
	if clone.Sequence <= state.ActualSequence {
		writeError(w, http.StatusConflict, "rollback_sequence_conflict", "signed rollback sequence is not newer than the installed release")
		return
	}
	updated, err := a.store.AssignAgentRelease(r.Context(), nodeID, clone.ID, input.ExpectedActualSequence, *input.ExpectedDesiredSequence)
	if errors.Is(err, ErrAgentUpdateStateChanged) {
		writeError(w, http.StatusConflict, "update_state_changed", err.Error())
		return
	}
	if errors.Is(err, ErrReleaseNotNewer) {
		writeError(w, http.StatusConflict, "actual_sequence_changed", "installed or assigned Agent release changed; reload state and retry")
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "rollback_assignment_failed", "could not assign signed rollback release")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (a *API) agentReleases(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		releases, err := a.store.ListAgentReleases(r.Context())
		respondStore(w, releases, err, http.StatusOK)
	case http.MethodPost:
		if a.releases == nil {
			writeError(w, http.StatusServiceUnavailable, "release_service_unavailable", "Agent release signing is not configured")
			return
		}
		if contentType := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]); contentType != "application/octet-stream" {
			writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "release body must use application/octet-stream")
			return
		}
		version, ok := singleQueryValue(r, "version", true)
		if !ok {
			writeError(w, http.StatusBadRequest, "validation_error", "version must be supplied exactly once")
			return
		}
		operatingSystem, ok := singleQueryValue(r, "os", false)
		if !ok {
			writeError(w, http.StatusBadRequest, "validation_error", "os may be supplied once")
			return
		}
		architecture, ok := singleQueryValue(r, "arch", false)
		if !ok {
			writeError(w, http.StatusBadRequest, "validation_error", "arch may be supplied once")
			return
		}
		release, err := a.releases.Create(r.Context(), version, operatingSystem, architecture, r.Body)
		var inputError *ReleaseInputError
		var tooLarge *ReleaseTooLargeError
		var sizeError *http.MaxBytesError
		if errors.As(err, &inputError) {
			writeError(w, http.StatusBadRequest, "validation_error", inputError.Error())
			return
		}
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "artifact_too_large", "Agent release exceeds 64 MiB")
			return
		}
		if errors.As(err, &sizeError) {
			writeError(w, http.StatusRequestEntityTooLarge, "artifact_too_large", "Agent release exceeds 64 MiB")
			return
		}
		respondStore(w, release, err, http.StatusCreated)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (a *API) agentReleaseSigningKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	if a.releases == nil {
		writeError(w, http.StatusServiceUnavailable, "release_service_unavailable", "Agent release signing is not configured")
		return
	}
	publicKey := a.releases.PublicKeyBase64()
	raw, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	fingerprint := sha256.Sum256(raw)
	writeJSON(w, http.StatusOK, map[string]string{
		"algorithm": "ed25519", "public_key": publicKey, "sha256": hex.EncodeToString(fingerprint[:]),
	})
}

func (a *API) agentRelease(w http.ResponseWriter, r *http.Request, releaseID string) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, "DELETE")
		return
	}
	if a.releases == nil {
		writeError(w, http.StatusServiceUnavailable, "release_service_unavailable", "Agent release signing is not configured")
		return
	}
	err := a.releases.Delete(r.Context(), releaseID)
	if errors.Is(err, ErrReleaseInUse) {
		writeError(w, http.StatusConflict, "release_in_use", "Agent release is installed or assigned")
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Agent release not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "release_delete_failed", "Agent release could not be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func singleQueryValue(r *http.Request, key string, required bool) (string, bool) {
	values, exists := r.URL.Query()[key]
	if !exists {
		return "", !required
	}
	if len(values) != 1 || (required && strings.TrimSpace(values[0]) == "") {
		return "", false
	}
	return values[0], true
}

type configRevisionInput struct {
	Config   string         `json:"config"`
	Note     string         `json:"note"`
	Metadata map[string]any `json:"metadata"`
}

func (a *API) configRevisions(w http.ResponseWriter, r *http.Request, nodeID string) {
	switch r.Method {
	case http.MethodGet:
		v, err := a.store.ListConfigRevisions(r.Context(), nodeID)
		respondStore(w, v, err, http.StatusOK)
	case http.MethodPost:
		var in configRevisionInput
		if !decode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Config) == "" || len(in.Config) > MaxManagedConfigBytes || len(in.Note) > 500 {
			writeError(w, 400, "validation_error", "config must be between 1 and 524288 bytes; note must not exceed 500 characters")
			return
		}
		if in.Metadata == nil {
			in.Metadata = map[string]any{}
		}
		v, err := a.store.CreateConfigRevision(r.Context(), nodeID, in.Config, strings.TrimSpace(in.Note), in.Metadata)
		respondStore(w, v, err, http.StatusCreated)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (a *API) configRevision(w http.ResponseWriter, r *http.Request, nodeID string, revision int64) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	v, err := a.store.GetConfigRevision(r.Context(), nodeID, revision)
	respondStore(w, v, err, http.StatusOK)
}

type renderedConfigResponse struct {
	NodeID string `json:"node_id"`
	HAProxyRenderResult
}

func (a *API) renderConfig(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	result, err := a.renderCurrentRoutes(r.Context(), nodeID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			respondStore(w, nil, err, http.StatusOK)
			return
		}
		status, code, message := renderErrorResponse(err)
		writeError(w, status, code, message)
		return
	}
	writeJSON(w, http.StatusOK, renderedConfigResponse{NodeID: nodeID, HAProxyRenderResult: result})
}

type configRevisionFromRoutesInput struct {
	Note string `json:"note"`
}

func (a *API) configRevisionFromRoutes(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in configRevisionFromRoutesInput
	if !decode(w, r, &in) {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if len(in.Note) > 500 {
		writeError(w, http.StatusBadRequest, "validation_error", "note must not exceed 500 characters")
		return
	}
	result, err := a.renderCurrentRoutes(r.Context(), nodeID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			respondStore(w, nil, err, http.StatusCreated)
			return
		}
		status, code, message := renderErrorResponse(err)
		writeError(w, status, code, message)
		return
	}
	if in.Note == "" {
		in.Note = "Собрано: " + routeRenderSummary(result)
	}
	revision, err := a.store.CreateConfigRevision(r.Context(), nodeID, result.Config, in.Note, renderMetadata(result))
	respondStore(w, revision, err, http.StatusCreated)
}

func (a *API) renderCurrentRoutes(ctx context.Context, nodeID string) (HAProxyRenderResult, error) {
	if _, err := a.store.GetNode(ctx, nodeID); err != nil {
		return HAProxyRenderResult{}, err
	}
	routes, err := a.store.ListRoutes(ctx, nodeID)
	if err != nil {
		return HAProxyRenderResult{}, err
	}
	agentVersion, err := a.store.GetNodeAgentVersion(ctx, nodeID)
	if err != nil {
		return HAProxyRenderResult{}, err
	}
	if err := checkKernelShaperRoutes(routes, agentVersion); err != nil {
		return HAProxyRenderResult{}, err
	}
	if err := checkLeastPingAgentRoutes(routes, agentVersion); err != nil {
		return HAProxyRenderResult{}, err
	}
	if err := checkIPWeightsAgentRoutes(routes, agentVersion); err != nil {
		return HAProxyRenderResult{}, err
	}
	if err := checkLeastConnAgentRoutes(routes, agentVersion); err != nil {
		return HAProxyRenderResult{}, err
	}
	if err := checkPPTrustedDomainsAgentRoutes(routes, agentVersion); err != nil {
		return HAProxyRenderResult{}, err
	}
	facts, err := a.store.GetNodeRenderFacts(ctx, nodeID)
	if err != nil {
		return HAProxyRenderResult{}, err
	}
	return RenderHAProxyConfigForNode(routes, facts)
}

// withStickyTableEffective adds sticky_table_entries_effective to GET
// responses. Missing node facts never fail the read.
func (a *API) withStickyTableEffective(ctx context.Context, nodeID string, routes []Route) {
	facts, err := a.store.GetNodeRenderFacts(ctx, nodeID)
	if err != nil {
		facts = NodeRenderFacts{}
	}
	annotateStickyTableEffective(routes, facts)
}

func (a *API) configState(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	v, err := a.store.GetConfigState(r.Context(), nodeID)
	respondStore(w, v, err, http.StatusOK)
}

type desiredRevisionInput struct {
	Revision int64 `json:"revision"`
}

func (a *API) desiredRevision(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, "PUT")
		return
	}
	var in desiredRevisionInput
	if !decode(w, r, &in) {
		return
	}
	if in.Revision < 1 {
		writeError(w, 400, "validation_error", "revision must be a positive integer")
		return
	}
	v, err := a.store.AssignDesiredRevision(r.Context(), nodeID, in.Revision)
	respondStore(w, v, err, http.StatusOK)
}

func (a *API) bootstrapNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if a.bootstrap == nil {
		writeError(w, http.StatusServiceUnavailable, "bootstrap_unavailable", "bootstrap is not configured")
		return
	}
	if requirement, ok := a.bootstrap.(interface{ RequiresAgentRelease() bool }); ok && requirement.RequiresAgentRelease() {
		if a.releases == nil {
			writeError(w, http.StatusConflict, "agent_release_required", "upload a signed Agent release before bootstrap")
			return
		}
		releases, err := a.store.ListAgentReleases(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "release_store_unavailable", "Agent releases are unavailable")
			return
		}
		if len(releases) == 0 {
			writeError(w, http.StatusConflict, "agent_release_required", "upload a signed Agent release before bootstrap")
			return
		}
	}
	var in bootstrap.Request
	defer in.ClearSecrets()
	if !decode(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if in.ReleaseID != "" && !validID(in.ReleaseID) {
		writeError(w, http.StatusBadRequest, "validation_error", "release_id must be a UUID")
		return
	}
	actor, _ := r.Context().Value(auditActorContextKey{}).(auditActor)
	job, err := a.bootstrapJobs.submit(in, func(ctx context.Context, request *bootstrap.Request) bootstrapJobOutcome {
		return a.runBootstrapJob(ctx, request, actor)
	})
	if err != nil {
		if errors.Is(err, errBootstrapJobLimit) {
			writeError(w, http.StatusTooManyRequests, "bootstrap_job_limit", "too many bootstrap jobs; wait for an active job to finish")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) bootstrapJob(w http.ResponseWriter, r *http.Request, jobID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	job, ok := a.bootstrapJobs.get(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "bootstrap_job_not_found", "bootstrap job not found or expired")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (a *API) runBootstrapJob(ctx context.Context, in *bootstrap.Request, actor auditActor) bootstrapJobOutcome {
	bootstrap.ReportProgress(ctx, "create_node")
	n, err := a.store.CreateNode(ctx, in.Name, in.Address, map[string]any{"agent_port": in.AgentPort, "ssh_port": in.SSHPort, "firewall_apply_allowed": in.AllowFirewallApply})
	if err != nil {
		slog.Error("bootstrap failed", "stage", "create_node", "error", err)
		return bootstrapJobOutcome{Stage: "create_node"}
	}
	cleanupNode := func(stage string) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if cleanupErr := a.store.DeleteNode(cleanupCtx, n.ID); cleanupErr != nil {
			slog.Error("bootstrap cleanup failed", "stage", stage, "node_id", n.ID, "error", cleanupErr)
		}
	}
	bootstrap.ReportProgress(ctx, "generate_token")
	token, tokenHash, tokenPrefix, err := newEnrollmentToken()
	if err != nil {
		slog.Error("bootstrap failed", "stage", "generate_token", "node_id", n.ID, "error", err)
		cleanupNode("generate_token")
		return bootstrapJobOutcome{Stage: "generate_token"}
	}
	bootstrap.ReportProgress(ctx, "store_token")
	if _, err = a.store.CreateEnrollmentToken(ctx, n.ID, tokenHash, tokenPrefix, time.Now().Add(nodeCredentialTTL)); err != nil {
		slog.Error("bootstrap failed", "stage", "store_token", "node_id", n.ID, "error", err)
		cleanupNode("store_token")
		return bootstrapJobOutcome{Stage: "store_token"}
	}
	in.NodeID, in.EnrollmentToken = n.ID, token
	in.OnReleaseSelected = func(selectionCtx context.Context, releaseID string) error {
		return a.store.PrepareBootstrapAgentRelease(selectionCtx, n.ID, releaseID)
	}
	if err = a.bootstrap.Install(ctx, *in); err != nil {
		cleanupNode("install")
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			slog.Error("bootstrap failed", "stage", "timeout", "node_id", n.ID)
			return bootstrapJobOutcome{Stage: "timeout"}
		}
		var se *bootstrap.StageError
		if errors.As(err, &se) {
			stage := safeBootstrapStage(se.Stage)
			diagnostic, exitCode := bootstrapErrorDiagnostic(err)
			slog.Error("bootstrap failed", "job_id", bootstrapJobID(ctx), "stage", stage, "reason", diagnostic, "exit_code", exitCode, "node_id", n.ID)
			return bootstrapJobOutcome{Stage: stage, DiagnosticCode: diagnostic, ExitCode: exitCode}
		}
		slog.Error("bootstrap failed", "stage", "install", "node_id", n.ID)
		return bootstrapJobOutcome{Stage: "install"}
	}
	event := AuditEvent{ActorType: actor.Type, ActorID: actor.ID, Action: "node.bootstrap", ResourceType: "node", ResourceID: n.ID, Details: map[string]any{"node_id": n.ID, "status": http.StatusCreated}}
	if event.ActorType == "" {
		event.ActorType = "unknown_admin"
	}
	event.Action, event.ResourceType, event.ResourceID = "node.bootstrap", "node", n.ID
	event.Details["node_id"] = n.ID
	if auditErr := a.store.AppendAudit(ctx, event); auditErr != nil {
		slog.Error("append bootstrap audit event failed", "node_id", n.ID, "error", auditErr)
	}
	return bootstrapJobOutcome{NodeID: n.ID, Stage: "installed"}
}

// reinstallNode runs the normal full installer against an existing node while
// preserving its control-plane identity and routes. SSH credentials remain only
// in the bounded bootstrap job closure and are scrubbed before terminal polling.
func (a *API) reinstallNode(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if a.bootstrap == nil {
		writeError(w, http.StatusServiceUnavailable, "bootstrap_unavailable", "bootstrap is not configured")
		return
	}
	if _, ok := a.bootstrap.(bootstrap.CredentialRotationFinalizer); !ok {
		writeError(w, http.StatusServiceUnavailable, "reinstall_rollback_unavailable", "bootstrap credential rollback is not configured")
		return
	}
	node, err := a.store.GetNode(r.Context(), nodeID)
	if err != nil {
		respondStore(w, nil, err, http.StatusOK)
		return
	}
	var in bootstrap.Request
	defer in.ClearSecrets()
	if !decode(w, r, &in) {
		return
	}
	in.Name, in.Address, in.NodeID = node.Name, node.Address, nodeID
	in.CredentialRotation = false
	in.Reinstall = true
	in.UseStoredSSHPort = in.SSHPort == 0
	applyExistingNodeBootstrapDefaults(&in, node)
	in.ReinstallBackupPath, err = bootstrap.NewReinstallBackupPath()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if err = in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	actor, _ := r.Context().Value(auditActorContextKey{}).(auditActor)
	sourceIP := requestSourceIP(r)
	job, err := a.bootstrapJobs.submitKeyed("reinstall:"+nodeID, in, func(ctx context.Context, request *bootstrap.Request) bootstrapJobOutcome {
		return a.runReinstallJob(ctx, request, actor, sourceIP)
	})
	if err != nil {
		if errors.Is(err, errBootstrapJobLimit) {
			writeError(w, http.StatusTooManyRequests, "bootstrap_job_limit", "too many bootstrap jobs; wait for an active job to finish")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func applyExistingNodeBootstrapDefaults(in *bootstrap.Request, node Node) {
	metadata, _ := node.Metadata.(map[string]any)
	if in.SSHPort == 0 {
		in.SSHPort = integerMetadata(metadata["ssh_port"])
	}
	if configuredAgentPort := integerMetadata(metadata["agent_port"]); configuredAgentPort > 0 {
		in.AgentPort = configuredAgentPort
	} else if in.AgentPort == 0 {
		in.AgentPort = integerMetadata(metadata["agent_port"])
	}
}

func requestSourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	return ""
}

func (a *API) runReinstallJob(ctx context.Context, in *bootstrap.Request, actor auditActor, sourceIP string) bootstrapJobOutcome {
	unlockCredentialChange, err := a.lockCredentialRenewalContext(ctx, in.NodeID)
	if err != nil {
		return bootstrapJobOutcome{Stage: "timeout"}
	}
	defer unlockCredentialChange()

	bootstrap.ReportProgress(ctx, "lookup_node")
	node, err := a.store.GetNode(ctx, in.NodeID)
	if err != nil {
		slog.Error("node reinstall failed", "stage", "lookup_node", "node_id", in.NodeID, "error", err)
		return bootstrapJobOutcome{Stage: "lookup_node"}
	}
	in.Name, in.Address = node.Name, node.Address
	if in.UseStoredSSHPort {
		if metadata, ok := node.Metadata.(map[string]any); ok {
			if port := integerMetadata(metadata["ssh_port"]); port > 0 {
				in.SSHPort = port
			}
		}
	}
	applyExistingNodeBootstrapDefaults(in, node)
	bootstrap.ReportProgress(ctx, "generate_token")
	token, tokenHash, tokenPrefix, err := newEnrollmentToken()
	if err != nil {
		slog.Error("node reinstall failed", "stage", "generate_token", "node_id", in.NodeID, "error", err)
		return bootstrapJobOutcome{Stage: "generate_token"}
	}
	bootstrap.ReportProgress(ctx, "store_token")
	credential, err := a.store.CreateEnrollmentToken(ctx, in.NodeID, tokenHash, tokenPrefix, time.Now().Add(nodeCredentialTTL))
	if err != nil {
		slog.Error("node reinstall failed", "stage", "store_token", "node_id", in.NodeID, "error", err)
		return bootstrapJobOutcome{Stage: "store_token"}
	}
	in.EnrollmentToken = token
	selectedReleaseID := strings.TrimSpace(in.ReleaseID)
	in.OnReleaseSelected = func(_ context.Context, releaseID string) error {
		selectedReleaseID = strings.TrimSpace(releaseID)
		return nil
	}
	if err = a.bootstrap.Install(ctx, *in); err != nil {
		stage := bootstrapErrorStage(ctx, err)
		diagnostic, exitCode := bootstrapErrorDiagnostic(err)
		rollbackRequired := stage == "install" || stage == "timeout"
		if rollbackRequired && !a.rollbackReinstallCredentials(*in, in.NodeID) {
			slog.Error("node reinstall failed", "stage", "rollback", "node_id", in.NodeID)
			return bootstrapJobOutcome{Stage: "rollback"}
		}
		if cleanupErr := a.revokeReinstallCredential(credential.ID); cleanupErr != nil {
			slog.Error("node reinstall credential cleanup failed", "node_id", in.NodeID, "error", cleanupErr)
			return bootstrapJobOutcome{Stage: "credential_cleanup"}
		}
		slog.Error("node reinstall failed", "job_id", bootstrapJobID(ctx), "stage", stage, "reason", diagnostic, "exit_code", exitCode, "node_id", in.NodeID)
		return bootstrapJobOutcome{Stage: stage, DiagnosticCode: diagnostic, ExitCode: exitCode}
	}

	bootstrap.ReportProgress(ctx, "verify_credential")
	verificationContext, verificationCancel := context.WithTimeout(ctx, reinstallVerifyTimeout)
	err = waitForEnrollmentTokenUse(verificationContext, a.store, credential.ID)
	verificationCancel()
	if err != nil {
		if !a.rollbackReinstallCredentials(*in, in.NodeID) {
			slog.Error("node reinstall heartbeat verification rollback failed", "node_id", in.NodeID, "error", err)
			return bootstrapJobOutcome{Stage: "rollback"}
		}
		if cleanupErr := a.revokeReinstallCredential(credential.ID); cleanupErr != nil {
			slog.Error("node reinstall credential cleanup failed", "node_id", in.NodeID, "error", cleanupErr)
			return bootstrapJobOutcome{Stage: "credential_cleanup"}
		}
		slog.Error("node reinstall heartbeat verification failed", "node_id", in.NodeID, "error", err)
		return bootstrapJobOutcome{Stage: "verify_credential"}
	}

	desiredFirewallMode := "observe"
	if in.AllowFirewallApply {
		desiredFirewallMode = "apply"
	}
	bootstrap.ReportProgress(ctx, "firewall_policy")
	policyContext, policyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err = a.store.UpdateFirewallPolicy(policyContext, in.NodeID, desiredFirewallMode)
	policyCancel()
	if err != nil {
		slog.Error("node reinstall firewall policy update failed", "node_id", in.NodeID, "mode", desiredFirewallMode, "error", err)
		if !a.rollbackReinstallCredentials(*in, in.NodeID) {
			return bootstrapJobOutcome{Stage: "rollback"}
		}
		if cleanupErr := a.revokeReinstallCredential(credential.ID); cleanupErr != nil {
			return bootstrapJobOutcome{Stage: "credential_cleanup"}
		}
		return bootstrapJobOutcome{Stage: "firewall_policy"}
	}

	bootstrap.ReportProgress(ctx, "revoke_credentials")
	revokeContext, revokeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = retryCredentialStoreOperation(revokeContext, func(operationContext context.Context) error {
		return a.store.RevokeOtherEnrollmentTokens(operationContext, in.NodeID, credential.ID)
	})
	revokeCancel()
	if err != nil {
		slog.Error("node reinstall superseded credential revoke failed", "node_id", in.NodeID, "error", err)
		if !a.rollbackReinstallCredentials(*in, in.NodeID) {
			return bootstrapJobOutcome{Stage: "rollback"}
		}
		if cleanupErr := a.revokeReinstallCredential(credential.ID); cleanupErr != nil {
			slog.Error("node reinstall credential cleanup failed", "node_id", in.NodeID, "error", cleanupErr)
			return bootstrapJobOutcome{Stage: "credential_cleanup"}
		}
		return bootstrapJobOutcome{Stage: "revoke_credentials"}
	}

	bootstrap.ReportProgress(ctx, "finalize")
	finalizer := a.bootstrap.(bootstrap.CredentialRotationFinalizer)
	finalizeContext, finalizeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	finalizeErr := finalizer.FinalizeCredentials(finalizeContext, *in)
	finalizeCancel()
	if finalizeErr != nil {
		// Credentials are already committed and old tokens revoked. Retaining a
		// root-only rollback backup is safer than claiming the reinstall failed.
		slog.Error("node reinstall backup cleanup failed", "node_id", in.NodeID, "error", finalizeErr)
	}
	if selectedReleaseID != "" {
		releaseContext, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = retryCredentialStoreOperation(releaseContext, func(operationContext context.Context) error {
			return a.store.PrepareBootstrapAgentRelease(operationContext, in.NodeID, selectedReleaseID)
		})
		releaseCancel()
		if err != nil {
			// The new Agent and credentials are already active. Do not report a
			// destructive reinstall failure for a recoverable control-plane state
			// sync; the operator can still assign the signed release explicitly.
			slog.Error("node reinstall release state sync failed", "node_id", in.NodeID, "release_id", selectedReleaseID, "error", err)
		}
	}
	event := AuditEvent{
		ActorType: actor.Type, ActorID: actor.ID, Action: "node.reinstall", ResourceType: "node", ResourceID: in.NodeID,
		Details: map[string]any{"node_id": in.NodeID, "status": http.StatusOK, "firewall_mode": desiredFirewallMode}, SourceIP: sourceIP,
	}
	if event.ActorType == "" {
		event.ActorType = "unknown_admin"
	}
	auditContext, auditCancel := context.WithTimeout(context.Background(), 10*time.Second)
	auditErr := retryCredentialStoreOperation(auditContext, func(operationContext context.Context) error {
		return a.store.AppendAudit(operationContext, event)
	})
	auditCancel()
	if auditErr != nil {
		slog.Error("append node reinstall audit event failed", "node_id", in.NodeID, "error", auditErr)
	}
	return bootstrapJobOutcome{NodeID: in.NodeID, Stage: "installed"}
}

func bootstrapErrorStage(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	var stageError *bootstrap.StageError
	if errors.As(err, &stageError) {
		return safeBootstrapStage(stageError.Stage)
	}
	return "install"
}

func bootstrapErrorDiagnostic(err error) (string, int) {
	var stageError *bootstrap.StageError
	if !errors.As(err, &stageError) {
		return "", 0
	}
	diagnostic := safeBootstrapDiagnostic(stageError.DiagnosticCode)
	exitCode := stageError.ExitCode
	if exitCode < 1 || exitCode > 255 {
		exitCode = 0
	}
	return diagnostic, exitCode
}

func (a *API) rollbackReinstallCredentials(in bootstrap.Request, nodeID string) bool {
	finalizer, ok := a.bootstrap.(bootstrap.CredentialRotationFinalizer)
	if !ok {
		return false
	}
	rollbackContext, rollbackCancel := context.WithTimeout(context.Background(), 45*time.Second)
	err := finalizer.RollbackCredentials(rollbackContext, in)
	rollbackCancel()
	if err != nil {
		slog.Error("node reinstall credential rollback failed", "node_id", nodeID, "error", err)
		return false
	}
	return true
}

func (a *API) revokeReinstallCredential(tokenID string) error {
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	return retryCredentialStoreOperation(cleanupContext, func(operationContext context.Context) error {
		return a.store.RevokeEnrollmentToken(operationContext, tokenID)
	})
}

func (a *API) rotateNodeCredentials(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if a.bootstrap == nil {
		writeError(w, http.StatusServiceUnavailable, "bootstrap_unavailable", "bootstrap is not configured")
		return
	}
	node, err := a.store.GetNode(r.Context(), nodeID)
	if err != nil {
		respondStore(w, nil, err, http.StatusOK)
		return
	}
	var in bootstrap.Request
	defer in.ClearSecrets()
	if !decode(w, r, &in) {
		return
	}
	in.Name, in.Address, in.NodeID = node.Name, node.Address, node.ID
	in.CredentialRotation = true
	if metadata, ok := node.Metadata.(map[string]any); ok {
		if port := integerMetadata(metadata["agent_port"]); port > 0 {
			in.AgentPort = port
		}
	}
	firewallPolicy, err := a.store.GetFirewallPolicy(r.Context(), nodeID)
	if err != nil {
		respondStore(w, nil, err, http.StatusOK)
		return
	}
	in.AllowFirewallApply = firewallPolicy.Mode == "apply"
	if err = in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	unlockCredentialChange := a.lockCredentialRenewal(nodeID)
	defer unlockCredentialChange()
	token, tokenHash, tokenPrefix, err := newEnrollmentToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	credential, err := a.store.CreateEnrollmentToken(r.Context(), nodeID, tokenHash, tokenPrefix, time.Now().Add(nodeCredentialTTL))
	if err != nil {
		respondStore(w, nil, err, http.StatusCreated)
		return
	}
	in.EnrollmentToken = token
	if err = a.bootstrap.Install(r.Context(), in); err != nil {
		if revokeErr := a.store.RevokeEnrollmentToken(context.Background(), credential.ID); revokeErr != nil {
			slog.Error("credential rotation cleanup failed", "node_id", nodeID, "error", revokeErr)
		}
		var stageError *bootstrap.StageError
		if errors.As(err, &stageError) {
			writeError(w, http.StatusBadGateway, "credential_rotation_failed", "credential rotation failed at "+stageError.Stage)
			return
		}
		writeError(w, http.StatusBadGateway, "credential_rotation_failed", "credential rotation failed")
		return
	}
	verificationContext, verificationCancel := context.WithTimeout(r.Context(), 20*time.Second)
	err = waitForEnrollmentTokenUse(verificationContext, a.store, credential.ID)
	verificationCancel()
	if err != nil {
		rollbackOK := false
		if finalizer, ok := a.bootstrap.(bootstrap.CredentialRotationFinalizer); ok {
			rollbackContext, rollbackCancel := context.WithTimeout(context.Background(), 45*time.Second)
			rollbackErr := finalizer.RollbackCredentials(rollbackContext, in)
			rollbackCancel()
			if rollbackErr == nil {
				rollbackOK = true
				if revokeErr := a.store.RevokeEnrollmentToken(context.Background(), credential.ID); revokeErr != nil {
					slog.Error("revoke unverified credential failed", "node_id", nodeID, "error", revokeErr)
				}
			} else {
				slog.Error("credential rotation rollback failed", "node_id", nodeID, "error", rollbackErr)
			}
		}
		slog.Error("credential rotation heartbeat verification failed", "node_id", nodeID, "rolled_back", rollbackOK, "error", err)
		writeError(w, http.StatusBadGateway, "credential_rotation_unverified", "new Agent credential did not authenticate to Panel; old credentials were not revoked")
		return
	}
	revokeContext, revokeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = retryCredentialStoreOperation(revokeContext, func(operationContext context.Context) error {
		return a.store.RevokeOtherEnrollmentTokens(operationContext, nodeID, credential.ID)
	})
	revokeCancel()
	if err != nil {
		slog.Error("revoke superseded node credentials failed", "node_id", nodeID, "error", err)
		rollbackOK := false
		revokedNewCredential := false
		if finalizer, ok := a.bootstrap.(bootstrap.CredentialRotationFinalizer); ok {
			rollbackContext, rollbackCancel := context.WithTimeout(context.Background(), 45*time.Second)
			rollbackErr := finalizer.RollbackCredentials(rollbackContext, in)
			rollbackCancel()
			if rollbackErr == nil {
				rollbackOK = true
				cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
				cleanupErr := retryCredentialStoreOperation(cleanupContext, func(operationContext context.Context) error {
					return a.store.RevokeEnrollmentToken(operationContext, credential.ID)
				})
				cleanupCancel()
				if cleanupErr == nil {
					revokedNewCredential = true
				} else {
					slog.Error("revoke rolled-back credential failed", "node_id", nodeID, "error", cleanupErr)
				}
			} else {
				slog.Error("credential rotation rollback after revoke failure failed", "node_id", nodeID, "error", rollbackErr)
			}
		}
		if rollbackOK && revokedNewCredential {
			writeError(w, http.StatusServiceUnavailable, "credential_rotation_rolled_back", "superseded credentials could not be revoked; previous Agent credentials were restored")
			return
		}
		writeError(w, http.StatusInternalServerError, "credential_rotation_incomplete", "credential revocation failed and a complete rollback could not be confirmed")
		return
	}
	if finalizer, ok := a.bootstrap.(bootstrap.CredentialRotationFinalizer); ok {
		finalizeContext, finalizeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if finalizeErr := finalizer.FinalizeCredentials(finalizeContext, in); finalizeErr != nil {
			slog.Error("credential rotation backup cleanup failed", "node_id", nodeID, "error", finalizeErr)
		}
		finalizeCancel()
	}
	writeJSON(w, http.StatusOK, map[string]any{"node_id": nodeID, "status": "rotated", "expires_at": credential.ExpiresAt})
}

func waitForEnrollmentTokenUse(ctx context.Context, store Store, tokenID string) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		used, err := store.EnrollmentTokenUsed(ctx, tokenID)
		if err != nil {
			return err
		}
		if used {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

const credentialStoreRetryAttempts = 3

func retryCredentialStoreOperation(ctx context.Context, operation func(context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < credentialStoreRetryAttempts; attempt++ {
		lastErr = operation(ctx)
		if lastErr == nil || errors.Is(lastErr, ErrNotFound) {
			return nil
		}
		if attempt == credentialStoreRetryAttempts-1 {
			break
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func integerMetadata(value any) int {
	switch typed := value.(type) {
	case float64:
		if typed >= 1 && typed <= 65535 && typed == float64(int(typed)) {
			return int(typed)
		}
	case int:
		if typed >= 1 && typed <= 65535 {
			return typed
		}
	case json.Number:
		if parsed, err := strconv.Atoi(typed.String()); err == nil && parsed >= 1 && parsed <= 65535 {
			return parsed
		}
	}
	return 0
}

type nodeInput struct {
	Name     string         `json:"name"`
	Address  string         `json:"address"`
	Metadata map[string]any `json:"metadata"`
}

type nodeOrderInput struct {
	NodeIDs []string `json:"node_ids"`
}

func (a *API) nodeOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, "PUT")
		return
	}
	var in nodeOrderInput
	if !decode(w, r, &in) {
		return
	}
	if len(in.NodeIDs) > 10000 {
		writeError(w, http.StatusBadRequest, "validation_error", "too many node IDs")
		return
	}
	value, err := a.store.ReorderNodes(r.Context(), in.NodeIDs)
	respondStore(w, value, err, http.StatusOK)
}

func (a *API) nodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		n, err := a.store.ListNodes(r.Context())
		respondStore(w, n, err, http.StatusOK)
	case http.MethodPost:
		var in nodeInput
		if !decode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Name) == "" || net.ParseIP(strings.TrimSpace(in.Address)) == nil {
			writeError(w, 400, "validation_error", "name and valid IP address are required")
			return
		}
		if in.Metadata == nil {
			in.Metadata = map[string]any{}
		}
		n, err := a.store.CreateNode(r.Context(), strings.TrimSpace(in.Name), strings.TrimSpace(in.Address), in.Metadata)
		respondStore(w, n, err, http.StatusCreated)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}
func (a *API) node(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		n, err := a.store.GetNode(r.Context(), id)
		respondStore(w, n, err, 200)
	case http.MethodPut:
		var in nodeInput
		if !decode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Name) == "" || net.ParseIP(strings.TrimSpace(in.Address)) == nil {
			writeError(w, 400, "validation_error", "name and valid IP address are required")
			return
		}
		if in.Metadata == nil {
			in.Metadata = map[string]any{}
		}
		n, err := a.store.UpdateNode(r.Context(), id, strings.TrimSpace(in.Name), strings.TrimSpace(in.Address), in.Metadata)
		respondStore(w, n, err, 200)
	case http.MethodDelete:
		err := a.store.DeleteNode(r.Context(), id)
		if err == nil {
			w.WriteHeader(204)
			return
		}
		respondStore(w, nil, err, 200)
	default:
		methodNotAllowed(w, "GET, PUT, DELETE")
	}
}

func (a *API) routes(w http.ResponseWriter, r *http.Request, nodeID string) {
	switch r.Method {
	case http.MethodGet:
		v, e := a.store.ListRoutes(r.Context(), nodeID)
		if e == nil {
			a.withStickyTableEffective(r.Context(), nodeID, v)
		}
		respondStore(w, v, e, 200)
	case http.MethodPost:
		var in routeInput
		if !decode(w, r, &in) {
			return
		}
		spec, err := validateRoute(in, false)
		if err != nil {
			writeError(w, 400, "validation_error", err.Error())
			return
		}
		v, e := a.store.CreateRoute(r.Context(), nodeID, spec)
		respondStore(w, v, e, 201)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

type routeOrderInput struct {
	RouteIDs []string `json:"route_ids"`
}

func (a *API) routeOrder(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, "PUT")
		return
	}
	var in routeOrderInput
	if !decode(w, r, &in) {
		return
	}
	if len(in.RouteIDs) > 10000 {
		writeError(w, http.StatusBadRequest, "validation_error", "too many route IDs")
		return
	}
	value, err := a.store.ReorderRoutes(r.Context(), nodeID, in.RouteIDs)
	respondStore(w, value, err, http.StatusOK)
}

type haproxyControlInput struct {
	Enabled            bool  `json:"enabled"`
	ExpectedGeneration int64 `json:"expected_generation"`
}

func (a *API) nodeHAProxy(w http.ResponseWriter, r *http.Request, nodeID string) {
	switch r.Method {
	case http.MethodGet:
		value, err := a.store.GetHAProxyControl(r.Context(), nodeID)
		respondStore(w, value, err, http.StatusOK)
	case http.MethodPut:
		var in haproxyControlInput
		if !decode(w, r, &in) {
			return
		}
		if in.ExpectedGeneration < 0 {
			writeError(w, http.StatusBadRequest, "validation_error", "expected_generation must be non-negative")
			return
		}
		value, err := a.store.UpdateHAProxyControl(r.Context(), nodeID, in.Enabled, in.ExpectedGeneration)
		respondStore(w, value, err, http.StatusAccepted)
	default:
		methodNotAllowed(w, "GET, PUT")
	}
}

func (a *API) nodeHAProxyRestart(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct {
		ExpectedGeneration int64 `json:"expected_generation"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedGeneration < 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "expected_generation must be non-negative")
		return
	}
	value, err := a.store.RestartHAProxy(r.Context(), nodeID, in.ExpectedGeneration)
	respondStore(w, value, err, http.StatusAccepted)
}
func (a *API) route(w http.ResponseWriter, r *http.Request, nodeID, id string) {
	switch r.Method {
	case http.MethodGet:
		v, e := a.store.GetRoute(r.Context(), nodeID, id)
		if e == nil {
			// «Авто» shares node RAM between all enabled auto tables.
			peers, err := a.store.ListRoutes(r.Context(), nodeID)
			if err != nil {
				peers = nil
			}
			routes := []Route{v}
			for _, peer := range peers {
				if peer.ID != v.ID {
					routes = append(routes, peer)
				}
			}
			a.withStickyTableEffective(r.Context(), nodeID, routes)
			v.StickyTableEntriesEffective = routes[0].StickyTableEntriesEffective
		}
		respondStore(w, v, e, 200)
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, 400, "invalid_json", "request body must be valid JSON")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var in routeInput
		if !decode(w, r, &in) {
			return
		}
		present, err := routeInputKeys(body)
		if err != nil {
			writeError(w, 400, "invalid_json", "request body must contain one JSON object")
			return
		}
		if routeInputOmitsStoredFields(present) {
			current, err := a.store.GetRoute(r.Context(), nodeID, id)
			if err != nil {
				respondStore(w, nil, err, http.StatusOK)
				return
			}
			mergeAbsentRouteFields(&in, present, current)
		}
		spec, err := validateRoute(in, true)
		if err != nil {
			writeError(w, 400, "validation_error", err.Error())
			return
		}
		v, e := a.store.UpdateRoute(r.Context(), nodeID, id, spec)
		respondStore(w, v, e, 200)
	case http.MethodPatch:
		var in routeEnabledInput
		if !decode(w, r, &in) {
			return
		}
		if in.Enabled == nil {
			writeError(w, 400, "validation_error", "enabled is required")
			return
		}
		if in.ExpectedVersion != nil && *in.ExpectedVersion < 1 {
			writeError(w, 400, "validation_error", "expected_version must be a positive integer")
			return
		}
		v, e := a.store.SetRouteEnabled(r.Context(), nodeID, id, *in.Enabled, in.ExpectedVersion)
		respondStore(w, v, e, 200)
	case http.MethodDelete:
		var expectedVersion *int64
		if values, supplied := r.URL.Query()["expected_version"]; supplied {
			if len(values) != 1 {
				writeError(w, http.StatusBadRequest, "validation_error", "expected_version must be a positive integer")
				return
			}
			value, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || value < 1 {
				writeError(w, http.StatusBadRequest, "validation_error", "expected_version must be a positive integer")
				return
			}
			expectedVersion = &value
		}
		result, e := a.store.DeleteRoute(r.Context(), nodeID, id, expectedVersion)
		if e != nil {
			respondStore(w, nil, e, http.StatusOK)
			return
		}
		if !result.Pending {
			w.WriteHeader(204)
			return
		}
		writeJSON(w, http.StatusAccepted, result.Route)
	default:
		methodNotAllowed(w, "GET, PUT, PATCH, DELETE")
	}
}

// routeEnabledInput is the PATCH /routes/{id} body. Only enabled can be
// changed; every other route field keeps its stored value.
type routeEnabledInput struct {
	Enabled         *bool  `json:"enabled"`
	ExpectedVersion *int64 `json:"expected_version"`
}

type enrollmentInput struct {
	TTLSeconds int `json:"ttl_seconds"`
}

func (a *API) enrollment(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in enrollmentInput
	if !decode(w, r, &in) {
		return
	}
	if in.TTLSeconds == 0 {
		in.TTLSeconds = int(nodeCredentialTTL / time.Second)
	}
	if in.TTLSeconds < 60 || time.Duration(in.TTLSeconds)*time.Second > nodeCredentialTTL {
		writeError(w, 400, "validation_error", "ttl_seconds must be between 60 and 71280000")
		return
	}
	unlockCredentialChange := a.lockCredentialRenewal(nodeID)
	defer unlockCredentialChange()
	token, hash, prefix, err := newEnrollmentToken()
	if err != nil {
		writeError(w, 500, "internal_error", "internal server error")
		return
	}
	t, err := a.store.CreateEnrollmentToken(r.Context(), nodeID, hash, prefix, time.Now().Add(time.Duration(in.TTLSeconds)*time.Second))
	if err != nil {
		respondStore(w, nil, err, 200)
		return
	}
	writeJSON(w, 201, map[string]any{"id": t.ID, "node_id": t.NodeID, "prefix": t.Prefix, "expires_at": t.ExpiresAt, "created_at": t.CreatedAt, "token": token})
}

func newEnrollmentToken() (token, hash, prefix string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	token = "nfe_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), token[:12], nil
}

func (a *API) heartbeat(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.agentCredentialIdentity(w, r, false)
	if !ok {
		return
	}
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		writeError(w, 401, "unauthorized", "enrollment bearer token required")
		return
	}
	var in Heartbeat
	if !decode(w, r, &in) {
		return
	}
	in.MTLSNodeID = identity.NodeID
	in.Credential = identity
	if len(in.Version) > 100 || len(in.Status) > 32 {
		writeError(w, 400, "validation_error", "invalid heartbeat fields")
		return
	}
	result, err := a.store.IngestHeartbeat(r.Context(), token, in)
	if errors.Is(err, ErrNotFound) {
		writeError(w, 401, "unauthorized", "invalid or expired enrollment token")
		return
	}
	if errors.Is(err, ErrInvalidTrafficMetrics) {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid HAProxy traffic metrics")
		return
	}
	if errors.Is(err, ErrInvalidObservedConfig) {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid observed configuration state")
		return
	}
	if errors.Is(err, ErrInvalidHeartbeatOrder) {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid heartbeat traffic ordering fields")
		return
	}
	if errors.Is(err, ErrInvalidUpdateMetrics) {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid Agent update metrics")
		return
	}
	if err != nil {
		slog.Error("heartbeat ingestion failed", "error", err)
		writeError(w, 500, "internal_error", "internal server error")
		return
	}
	if identity.NodeID != "" && result.NodeID != identity.NodeID {
		writeError(w, http.StatusUnauthorized, "node_identity_mismatch", "client certificate does not match enrollment token")
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (a *API) configReport(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.agentCredentialIdentity(w, r, false)
	if !ok {
		return
	}
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		writeError(w, 401, "unauthorized", "enrollment bearer token required")
		return
	}
	var in ApplyReport
	if !decode(w, r, &in) {
		return
	}
	in.MTLSNodeID = identity.NodeID
	in.Credential = identity
	validState := in.State == "applying" || in.State == "applied" || in.State == "failed" || in.State == "rolled_back"
	if in.Revision < 1 || !validState || !validReportCode(in.Error) || (in.ActualRevision != nil && *in.ActualRevision < 1) {
		writeError(w, 400, "validation_error", "invalid revision, state, actual_revision or error")
		return
	}
	if in.Details == nil {
		in.Details = map[string]any{}
	}
	details, err := json.Marshal(in.Details)
	if err != nil || len(details) > MaxReportDetailsBytes {
		writeError(w, 400, "validation_error", "details must be a JSON object up to 16384 bytes")
		return
	}
	v, err := a.store.IngestApplyReport(r.Context(), token, in)
	if errors.Is(err, ErrNotFound) {
		writeError(w, 401, "unauthorized", "invalid token or configuration revision")
		return
	}
	if err != nil {
		writeError(w, 500, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

const (
	maxCredentialCSRBytes = 8 << 10
	credentialConfirmTTL  = 24 * time.Hour
)

type credentialRenewalInput struct {
	RenewalID       string `json:"renewal_id"`
	CSRPEM          string `json:"csr_pem"`
	NextTokenHash   string `json:"next_token_sha256"`
	NextTokenPrefix string `json:"next_token_prefix"`
}

type credentialRenewalOutput struct {
	RenewalID         string    `json:"renewal_id"`
	CertificatePEM    string    `json:"certificate_pem"`
	CertificateSHA256 string    `json:"certificate_sha256"`
	Serial            string    `json:"serial"`
	NotBefore         time.Time `json:"not_before"`
	NotAfter          time.Time `json:"not_after"`
	ConfirmBy         time.Time `json:"confirm_by"`
}

func (a *API) credentialRenewal(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.agentCredentialIdentity(w, r, true)
	if !ok {
		return
	}
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "active enrollment bearer token required")
		return
	}
	var input credentialRenewalInput
	if !decode(w, r, &input) {
		return
	}
	input.RenewalID = strings.TrimSpace(input.RenewalID)
	input.NextTokenHash = strings.TrimSpace(input.NextTokenHash)
	input.NextTokenPrefix = strings.TrimSpace(input.NextTokenPrefix)
	if !validRenewalID(input.RenewalID) || !validSHA256Hex(input.NextTokenHash) || !validTokenPrefix(input.NextTokenPrefix) {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid renewal_id, next_token_sha256 or next_token_prefix")
		return
	}
	currentTokenHash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare([]byte(input.NextTokenHash), []byte(hex.EncodeToString(currentTokenHash[:]))) == 1 {
		writeError(w, http.StatusBadRequest, "validation_error", "next credential must use a new bearer token")
		return
	}
	csr, csrDER, err := parseCredentialCSR(input.CSRPEM)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_csr", "valid Ed25519 certificate request required")
		return
	}
	csrHash := sha256.Sum256(csrDER)
	request := CredentialRenewalRequest{
		RenewalID: input.RenewalID, CSRHash: hex.EncodeToString(csrHash[:]), CSRDER: csrDER,
		NextTokenHash: input.NextTokenHash, NextTokenPrefix: input.NextTokenPrefix,
	}
	unlock := a.lockCredentialRenewal(identity.NodeID)
	defer unlock()
	existing, err := a.store.AuthorizeCredentialRenewal(r.Context(), token, identity, request)
	if err != nil {
		writeCredentialRenewalError(w, err)
		return
	}
	if existing != nil {
		output, err := credentialRenewalOutputFromRecord(*existing)
		if err != nil {
			slog.Error("stored credential renewal certificate is invalid", "renewal_id", input.RenewalID)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		writeJSON(w, http.StatusOK, output)
		return
	}
	if a.credentialIssuer == nil {
		writeError(w, http.StatusServiceUnavailable, "credential_issuer_unavailable", "credential issuer is unavailable")
		return
	}
	certificatePEM, err := a.credentialIssuer.IssueCSR(identity.NodeID, csr)
	if errors.Is(err, bootstrap.ErrInvalidNodeCSR) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_csr", "valid Ed25519 certificate request required")
		return
	}
	if errors.Is(err, bootstrap.ErrAgentCAExpiresSoon) {
		writeError(w, http.StatusServiceUnavailable, "ca_expires_soon", "Agent CA expires too soon")
		return
	}
	if err != nil {
		slog.Error("issue Agent renewal certificate failed", "node_id", identity.NodeID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	certificate, err := validateIssuedCredentialCertificate(certificatePEM, identity.NodeID, csr)
	if err != nil {
		slog.Error("credential issuer returned invalid certificate", "node_id", identity.NodeID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	fingerprint := sha256.Sum256(certificate.Raw)
	now := time.Now().UTC()
	confirmBy := now.Add(credentialConfirmTTL)
	if confirmBy.After(certificate.NotAfter) {
		confirmBy = certificate.NotAfter.UTC()
	}
	record, created, err := a.store.CreateCredentialRenewal(r.Context(), token, identity, CredentialRenewalCandidate{
		CredentialRenewalRequest: request,
		CertificateSHA256:        hex.EncodeToString(fingerprint[:]), CertificateSerial: certificate.SerialNumber.String(),
		CertificateDER: append([]byte(nil), certificate.Raw...), CertificateNotAfter: certificate.NotAfter.UTC(), ConfirmBy: confirmBy,
	})
	if err != nil {
		writeCredentialRenewalError(w, err)
		return
	}
	output, err := credentialRenewalOutputFromRecord(record)
	if err != nil {
		slog.Error("created credential renewal certificate is invalid", "renewal_id", input.RenewalID)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, output)
}

func (a *API) lockCredentialRenewal(nodeID string) func() {
	unlock, _ := a.lockCredentialRenewalContext(context.Background(), nodeID)
	return unlock
}

func (a *API) lockCredentialRenewalContext(ctx context.Context, nodeID string) (func(), error) {
	value, _ := a.credentialRenewalLocks.LoadOrStore(nodeID, &credentialChangeLock{held: make(chan struct{}, 1)})
	lock := value.(*credentialChangeLock)
	select {
	case lock.held <- struct{}{}:
		return func() { <-lock.held }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *API) confirmCredentialRenewal(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.agentCredentialIdentity(w, r, true)
	if !ok {
		return
	}
	renewalID := strings.TrimSpace(r.PathValue("renewal_id"))
	if !validRenewalID(renewalID) {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid renewal_id")
		return
	}
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "candidate enrollment bearer token required")
		return
	}
	unlock := a.lockCredentialRenewal(identity.NodeID)
	defer unlock()
	record, err := a.store.ConfirmCredentialRenewal(r.Context(), token, identity, renewalID)
	if err != nil {
		writeCredentialRenewalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"renewal_id": record.RenewalID, "status": "active", "activated_at": record.ActivatedAt,
	})
}

func parseCredentialCSR(value string) (*x509.CertificateRequest, []byte, error) {
	if len(value) == 0 || len([]byte(value)) > maxCredentialCSRBytes {
		return nil, nil, bootstrap.ErrInvalidNodeCSR
	}
	block, rest := pem.Decode([]byte(value))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 || len(block.Bytes) > maxCredentialCSRBytes {
		return nil, nil, bootstrap.ErrInvalidNodeCSR
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return nil, nil, bootstrap.ErrInvalidNodeCSR
	}
	publicKey, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, nil, bootstrap.ErrInvalidNodeCSR
	}
	return csr, append([]byte(nil), block.Bytes...), nil
}

func validateIssuedCredentialCertificate(value []byte, nodeID string, csr *x509.CertificateRequest) (*x509.Certificate, error) {
	block, rest := pem.Decode(value)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("issuer returned invalid certificate PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || certificate.SerialNumber == nil || certificate.SerialNumber.Sign() <= 0 {
		return nil, errors.New("issuer returned invalid certificate")
	}
	issuedPublicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	requestedPublicKey, requestedOK := csr.PublicKey.(ed25519.PublicKey)
	if !ok || !requestedOK || !bytes.Equal(issuedPublicKey, requestedPublicKey) || certificate.Subject.CommonName != nodeID {
		return nil, errors.New("issuer returned mismatched node identity")
	}
	if len(certificate.DNSNames) != 0 || len(certificate.EmailAddresses) != 0 || len(certificate.IPAddresses) != 0 || len(certificate.URIs) != 0 {
		return nil, errors.New("issuer copied untrusted CSR names")
	}
	clientAuth := len(certificate.ExtKeyUsage) == 1
	for _, usage := range certificate.ExtKeyUsage {
		clientAuth = clientAuth && usage == x509.ExtKeyUsageClientAuth
	}
	now := time.Now().UTC()
	if !clientAuth || certificate.KeyUsage != x509.KeyUsageDigitalSignature || certificate.IsCA ||
		certificate.NotBefore.After(now.Add(5*time.Minute)) || !certificate.NotAfter.After(now) ||
		len(certificate.UnhandledCriticalExtensions) != 0 {
		return nil, errors.New("issuer returned unusable client certificate")
	}
	return certificate, nil
}

func credentialRenewalOutputFromRecord(record CredentialRenewalRecord) (credentialRenewalOutput, error) {
	certificate, err := x509.ParseCertificate(record.CertificateDER)
	if err != nil || certificate.SerialNumber == nil || certificate.SerialNumber.String() != record.CertificateSerial {
		return credentialRenewalOutput{}, errors.New("stored renewal certificate metadata mismatch")
	}
	return credentialRenewalOutput{
		RenewalID:         record.RenewalID,
		CertificatePEM:    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: record.CertificateDER})),
		CertificateSHA256: record.CertificateSHA256, Serial: record.CertificateSerial,
		NotBefore: certificate.NotBefore.UTC(), NotAfter: record.CertificateNotAfter.UTC(), ConfirmBy: record.ConfirmBy.UTC(),
	}, nil
}

func writeCredentialRenewalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credential pair")
	case errors.Is(err, ErrCredentialRenewalNotDue):
		writeError(w, http.StatusConflict, "renewal_not_due", "credential renewal is not due")
	case errors.Is(err, ErrCredentialRenewalInProgress):
		writeError(w, http.StatusConflict, "renewal_in_progress", "another credential renewal is pending")
	case errors.Is(err, ErrCredentialRenewalRateLimited):
		w.Header().Set("Retry-After", strconv.Itoa(int(credentialRenewalMinimumInterval/time.Second)))
		writeError(w, http.StatusTooManyRequests, "renewal_rate_limited", "credential renewal rate limit exceeded")
	case errors.Is(err, ErrCredentialRenewalIdempotency):
		writeError(w, http.StatusConflict, "idempotency_conflict", "renewal_id was already used with different input")
	case errors.Is(err, ErrCredentialRenewalExpired):
		writeError(w, http.StatusConflict, "renewal_expired", "credential renewal confirmation expired")
	default:
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, "credential_conflict", "candidate credential conflicts with existing state")
			return
		}
		slog.Error("credential renewal store operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func validRenewalID(value string) bool {
	return validID(value) && value == strings.ToLower(value) && value[14] == '4' && strings.ContainsRune("89ab", rune(value[19]))
}

func (a *API) agentUpdateArtifact(w http.ResponseWriter, r *http.Request) {
	if a.releases == nil {
		writeError(w, http.StatusServiceUnavailable, "release_service_unavailable", "Agent release storage is not configured")
		return
	}
	identity, ok := a.agentCredentialIdentity(w, r, false)
	if !ok {
		return
	}
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "enrollment bearer token required")
		return
	}
	sequence, err := strconv.ParseInt(r.PathValue("sequence"), 10, 64)
	if err != nil || sequence < 1 {
		writeError(w, http.StatusBadRequest, "validation_error", "update sequence must be a positive integer")
		return
	}
	release, err := a.store.GetAssignedAgentRelease(r.Context(), token, identity, sequence)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "assigned Agent release not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	artifact, err := a.releases.OpenArtifact(release.ArtifactPath)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusServiceUnavailable, "artifact_unavailable", "assigned Agent artifact is unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	defer artifact.Close()
	info, err := artifact.Stat()
	if err != nil || info.Size() != release.SizeBytes {
		writeError(w, http.StatusServiceUnavailable, "artifact_unavailable", "assigned Agent artifact is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(release.SizeBytes, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, artifact, release.SizeBytes)
}

func (a *API) agentNodeIdentity(w http.ResponseWriter, r *http.Request) (string, bool) {
	identity, ok := a.agentCredentialIdentity(w, r, false)
	return identity.NodeID, ok
}

func (a *API) agentCredentialIdentity(w http.ResponseWriter, r *http.Request, required bool) (AgentCredentialIdentity, bool) {
	if r.TLS == nil {
		if required || a.requireAgentMTLS {
			writeError(w, http.StatusUnauthorized, "mtls_required", "mutually authenticated TLS is required")
			return AgentCredentialIdentity{}, false
		}
		return AgentCredentialIdentity{}, true
	}
	if len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		writeError(w, http.StatusUnauthorized, "invalid_client_certificate", "valid node client certificate is required")
		return AgentCredentialIdentity{}, false
	}
	leaf := r.TLS.PeerCertificates[0]
	nodeID := strings.ToLower(strings.TrimSpace(leaf.Subject.CommonName))
	if !validID(nodeID) {
		writeError(w, http.StatusUnauthorized, "invalid_client_certificate", "client certificate must identify one node")
		return AgentCredentialIdentity{}, false
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	serial := ""
	if leaf.SerialNumber != nil {
		serial = leaf.SerialNumber.String()
	}
	return AgentCredentialIdentity{
		NodeID: nodeID, CertificateSHA256: hex.EncodeToString(fingerprint[:]),
		CertificateSerial: serial, CertificateNotAfter: leaf.NotAfter.UTC(),
	}, true
}

func validReportCode(value string) bool {
	if len(value) > 100 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

func bearer(v string) string {
	p := strings.SplitN(v, " ", 2)
	if len(p) == 2 && strings.EqualFold(p[0], "Bearer") {
		return strings.TrimSpace(p[1])
	}
	return ""
}
func validID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	if _, ok := dst.(*Heartbeat); ok {
		// Preserve cumulative counters exactly instead of routing them through
		// float64 before traffic accounting.
		dec.UseNumber()
	}
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, 400, "invalid_json", "request body must be valid JSON")
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid_json", "request body must contain one JSON object")
		return false
	}
	return true
}
func respondStore(w http.ResponseWriter, v any, err error, status int) {
	if errors.Is(err, ErrNotFound) {
		writeError(w, 404, "not_found", "resource not found")
		return
	}
	if errors.Is(err, ErrRouteVersionConflict) {
		writeError(w, http.StatusConflict, "stale_route_version", "route was changed by another operation; reload it and retry")
		return
	}
	if errors.Is(err, ErrOrderConflict) {
		writeError(w, http.StatusConflict, "stale_order", "resource list changed; reload and retry")
		return
	}
	if errors.Is(err, ErrControlGenerationConflict) {
		writeError(w, http.StatusConflict, "stale_haproxy_control", "HAProxy control state changed; reload and retry")
		return
	}
	if errors.Is(err, ErrControlUnsupported) {
		writeError(w, http.StatusConflict, "haproxy_control_unsupported", "update Node Agent before controlling HAProxy")
		return
	}
	if errors.Is(err, ErrHAProxyDisabled) {
		writeError(w, http.StatusConflict, "haproxy_disabled", "enable HAProxy before requesting a restart")
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, http.StatusConflict, "already_exists", "resource already exists")
		return
	}
	var kernelErr *KernelShaperUnsupportedError
	if errors.As(err, &kernelErr) {
		writeError(w, http.StatusUnprocessableEntity, kernelShaperRequiresAgentCode, kernelErr.Error())
		return
	}
	var leastPingErr *LeastPingUnsupportedError
	if errors.As(err, &leastPingErr) {
		writeError(w, http.StatusUnprocessableEntity, leastPingRequiresAgentCode, leastPingErr.Error())
		return
	}
	var ipWeightsErr *IPWeightsUnsupportedError
	if errors.As(err, &ipWeightsErr) {
		writeError(w, http.StatusUnprocessableEntity, ipWeightsRequiresAgentCode, ipWeightsErr.Error())
		return
	}
	var leastConnErr *LeastConnToleranceUnsupportedError
	if errors.As(err, &leastConnErr) {
		writeError(w, http.StatusUnprocessableEntity, leastConnToleranceRequiresAgentCode, leastConnErr.Error())
		return
	}
	var ppTrustedErr *PPTrustedDomainsUnsupportedError
	if errors.As(err, &ppTrustedErr) {
		writeError(w, http.StatusUnprocessableEntity, ppTrustedDomainsRequiresAgentCode, ppTrustedErr.Error())
		return
	}
	var routeSetErr *RouteSetError
	if errors.As(err, &routeSetErr) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_route_set", routeSetErr.Error())
		return
	}
	if err != nil {
		slog.Error("panel store operation failed", "error", err)
		writeError(w, 500, "internal_error", "internal server error")
		return
	}
	writeJSON(w, status, v)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, 405, "method_not_allowed", "method not allowed")
}
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(1 << 20)
		if r.Method == http.MethodPost && strings.TrimRight(r.URL.Path, "/") == "/api/v1/agent-releases" {
			limit = MaxAgentReleaseArtifactBytes + 1
		} else if r.Method == http.MethodPost && strings.TrimRight(r.URL.Path, "/") == "/agent/v1/credential-renewals" {
			limit = 16 << 10
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
func contextTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
