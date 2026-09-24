package agent

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	Config  Config
	Manager *ConfigManager
	Updater *UpdateVerifier
	Version string
	started time.Time
}

func NewServer(cfg Config, manager *ConfigManager, version string) *Server {
	return &Server{Config: cfg, Manager: manager, Version: version, started: time.Now()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "version": s.Version})
	})
	mux.HandleFunc("GET /v1/info", s.auth(func(w http.ResponseWriter, r *http.Request) {
		actualRevision, _ := s.Manager.ActualRevision()
		updateMode := UpdateModeOff
		if s.Updater != nil {
			updateMode = s.Updater.Mode()
		}
		writeJSON(w, 200, map[string]any{"agent_version": s.Version, "haproxy_version": HAProxyVersion(r.Context(), s.Manager.Runner, s.Config.HAProxyBinary), "uptime_seconds": int64(time.Since(s.started).Seconds()), "managed_config": s.Config.ManagedConfig, "actual_revision": actualRevision, "auth_mode": "bearer", "self_update_mode": updateMode})
	}))
	mux.HandleFunc("GET /v1/stats", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		stats, err := CollectStats()
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, stats)
	}))
	mux.HandleFunc("POST /v1/config/validate", s.auth(s.configHandler(false)))
	mux.HandleFunc("POST /v1/config/apply", s.auth(s.configHandler(true)))
	mux.HandleFunc("POST /v1/config/rollback", s.auth(s.rollbackHandler()))
	mux.HandleFunc("POST /v1/update/verify", s.auth(s.updateVerifyHandler()))
	return mux
}

func (s *Server) updateVerifyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Updater == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "update_verifier_unavailable"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var manifest UpdateManifest
		if err := decoder.Decode(&manifest); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_manifest_json"})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_manifest_json"})
			return
		}
		result, err := s.Updater.Verify(r.Context(), manifest)
		if err != nil {
			code := "verification_failed"
			var verificationError *UpdateVerificationError
			if errors.As(err, &verificationError) {
				code = verificationError.Code
			}
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": code, "verification": result})
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("Authorization")
		expected := "Bearer " + s.Config.Token
		if s.Config.Token == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}
func (s *Server) configHandler(apply bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		var req struct {
			Config   string `json:"config"`
			Revision string `json:"revision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_json"})
			return
		}
		if strings.TrimSpace(req.Config) == "" {
			writeJSON(w, 400, map[string]string{"error": "config is required"})
			return
		}
		if len(req.Config) > MaxManagedConfigBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "config_too_large"})
			return
		}
		revision := normalizedRevision([]byte(req.Config), req.Revision)
		if err := validateRevision(revision); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_revision"})
			return
		}
		if apply {
			result, _, err := s.Manager.ApplyRevision(r.Context(), []byte(req.Config), req.Revision)
			if err != nil {
				writeJSON(w, 422, map[string]string{"error": "apply_failed"})
				return
			}
			writeJSON(w, 200, map[string]any{"valid": true, "applied": !result.Idempotent, "idempotent": result.Idempotent, "actual_revision": result.Revision})
			return
		}
		if err := s.Manager.Validate(r.Context(), []byte(req.Config)); err != nil {
			writeJSON(w, 422, map[string]string{"error": "validation_failed"})
			return
		}
		writeJSON(w, 200, map[string]any{"valid": true, "revision": revision})
	}
}

func (s *Server) rollbackHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		revision, err := s.Manager.Rollback(r.Context())
		if err != nil {
			writeJSON(w, 409, map[string]string{"error": "rollback_unavailable"})
			return
		}
		writeJSON(w, 200, map[string]any{"rolled_back": true, "actual_revision": revision})
	}
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
