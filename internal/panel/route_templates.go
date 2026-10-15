package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Templates contain portable route intent, never runtime state or node identity.
type RouteTemplate struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Config      json.RawMessage `json:"config"`
	Version     int64           `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type routeTemplateStore interface {
	ListRouteTemplates(context.Context) ([]RouteTemplate, error)
	GetRouteTemplate(context.Context, string) (RouteTemplate, error)
	SaveRouteTemplate(context.Context, string, string, string, json.RawMessage, int64) (RouteTemplate, error)
	DeleteRouteTemplate(context.Context, string, int64) error
}

const templateColumns = `id::text,name,description,config,version,created_at,updated_at`

func scanTemplate(row pgx.Row) (RouteTemplate, error) {
	var v RouteTemplate
	err := row.Scan(&v.ID, &v.Name, &v.Description, &v.Config, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return v, err
}
func (s *PGStore) ListRouteTemplates(ctx context.Context) ([]RouteTemplate, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+templateColumns+` FROM route_templates ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RouteTemplate{}
	for rows.Next() {
		v, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PGStore) GetRouteTemplate(ctx context.Context, id string) (RouteTemplate, error) {
	return scanTemplate(s.pool.QueryRow(ctx, `SELECT `+templateColumns+` FROM route_templates WHERE id=$1`, id))
}
func (s *PGStore) SaveRouteTemplate(ctx context.Context, id, name, description string, config json.RawMessage, version int64) (RouteTemplate, error) {
	if id == "" {
		return scanTemplate(s.pool.QueryRow(ctx, `INSERT INTO route_templates(name,description,config) VALUES($1,$2,$3) RETURNING `+templateColumns, name, description, config))
	}
	v, err := scanTemplate(s.pool.QueryRow(ctx, `UPDATE route_templates SET name=$2,description=$3,config=$4,version=version+1,updated_at=now() WHERE id=$1 AND version=$5 RETURNING `+templateColumns, id, name, description, config, version))
	if errors.Is(err, ErrNotFound) {
		if _, e := s.GetRouteTemplate(ctx, id); e == nil {
			return v, ErrRouteVersionConflict
		} else {
			return v, e
		}
	}
	return v, err
}
func (s *PGStore) DeleteRouteTemplate(ctx context.Context, id string, version int64) error {
	result, err := s.pool.Exec(ctx, `DELETE FROM route_templates WHERE id=$1 AND version=$2`, id, version)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	if _, err = s.GetRouteTemplate(ctx, id); err != nil {
		return err
	}
	return ErrRouteVersionConflict
}

// Decode only intent fields; deliberately drop revision, quota usage, IDs and deployment state.
func portableRouteInput(route Route) (routeInput, error) {
	data, err := json.Marshal(route)
	if err != nil {
		return routeInput{}, err
	}
	var in routeInput
	if err = json.Unmarshal(data, &in); err != nil {
		return in, err
	}
	in.BalanceTolerance = nil
	in.ExpectedVersion = nil
	in.Enabled = new(bool)
	// Preserve explicit roles, including multiple primaries and reserve-first layouts.
	if len(in.Servers) > 0 {
		in.BalanceMode = ""
	}
	return in, nil
}
func validatedTemplateConfig(in routeInput) (json.RawMessage, error) {
	in.ExpectedVersion = nil
	in.Enabled = new(bool)
	spec, err := validateRoute(in, false)
	if err != nil {
		return nil, err
	}
	ipv6 := !spec.ClientIPv4Only
	canonical := routeInput{
		Name: spec.Name, Hostname: spec.Hostname, ListenerIP: spec.ListenerIP,
		ListenerPort: &spec.ListenerPort, MatchMode: spec.MatchMode, SNIs: spec.SNIs,
		Fallback: spec.Fallback, TargetType: spec.TargetType, TargetHost: spec.TargetHost,
		TargetPort: spec.TargetPort, DNSPool: spec.DNSPool, UnixSocketPath: spec.UnixSocketPath,
		HealthCheck: &spec.HealthCheck, ProxyProtocol: spec.ProxyProtocol, AcceptProxyFrom: spec.AcceptProxyFrom,
		QuotaBytes: spec.QuotaBytes, QuotaAction: spec.QuotaAction, QuotaPeriod: spec.QuotaPeriod,
		ClientUploadMbps: spec.ClientUploadMbps, ClientDownloadMbps: spec.ClientDownloadMbps,
		Enabled: new(bool), CustomFragment: spec.CustomFragment,
		StickyEnabled: spec.StickyEnabled, BalanceMode: spec.BalanceMode, ShaperMode: spec.ShaperMode,
		StickyMode: spec.StickyMode, StickyTTL: spec.StickyTTL,
		BalanceAlgorithm: spec.BalanceAlgorithm, BalanceRandomDraws: spec.BalanceRandomDraws,
		LeastPingTolerance: &spec.LeastPingTolerance, LeastPingToleranceMS: spec.LeastPingToleranceMS,
		StickyHash: spec.StickyHash, StickyHashBalanceFactor: spec.StickyHashBalanceFactor,
		StickyTableEntries: spec.StickyTableEntries, StickyIPv6Prefix: spec.StickyIPv6Prefix,
		ClientIPv6: &ipv6, Slowstart: spec.Slowstart,
	}
	for _, server := range spec.Servers {
		canonical.Servers = append(canonical.Servers, routeServerInput{
			Position: server.Position, Name: server.Name, TargetType: server.TargetType,
			Host: server.Host, Port: server.Port, UnixSocketPath: server.UnixSocketPath,
			Backup: server.Backup, DNSPool: server.DNSPool, PreferredIP: server.PreferredIP,
			Weight: server.Weight, Cost: server.Cost, IPWeights: server.IPWeights,
		})
	}
	if len(canonical.Servers) > 0 {
		canonical.BalanceMode = ""
	}
	return json.Marshal(struct {
		SchemaVersion int `json:"schema_version"`
		routeInput
	}{routeTemplateSchemaVersion, canonical})
}

// routeTemplateSchemaVersion is the format of route_templates.config. It is
// independent of the template "version" column, which is only the optimistic
// lock revision. Configs stored before schema_version existed count as 1.
const routeTemplateSchemaVersion = 1

// templateRouteInput decodes a stored template config, refusing formats
// written by a newer panel.
func templateRouteInput(raw json.RawMessage) (routeInput, error) {
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return routeInput{}, err
	}
	if head.SchemaVersion > routeTemplateSchemaVersion {
		return routeInput{}, &templateSchemaError{head.SchemaVersion}
	}
	var in routeInput
	err := json.Unmarshal(raw, &in)
	return in, err
}

type templateSchemaError struct{ version int }

func (e *templateSchemaError) Error() string {
	return fmt.Sprintf("template schema_version %d is not supported by this panel (max %d); update the panel", e.version, routeTemplateSchemaVersion)
}

// templateConfigInput is a route intent as stored in a template; a config
// read back from GET may be sent unchanged.
type templateConfigInput struct {
	SchemaVersion int `json:"schema_version"`
	routeInput
}

type templateInput struct {
	Name            string               `json:"name"`
	Description     string               `json:"description"`
	Config          *templateConfigInput `json:"config"`
	SourceNodeID    string               `json:"source_node_id"`
	SourceRouteID   string               `json:"source_route_id"`
	ExpectedVersion int64                `json:"expected_version"`
}

func (a *API) routeTemplates(w http.ResponseWriter, r *http.Request, parts []string) {
	store, ok := a.store.(routeTemplateStore)
	if !ok {
		writeError(w, 501, "unsupported", "route template storage is unavailable")
		return
	}
	id := ""
	if len(parts) > 1 {
		id = parts[1]
		if !validID(id) {
			writeError(w, 400, "validation_error", "invalid template id")
			return
		}
	}
	if len(parts) == 3 && parts[2] == "apply" {
		a.applyRouteTemplate(w, r, store, id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if id == "" {
			v, e := store.ListRouteTemplates(r.Context())
			respondStore(w, v, e, 200)
		} else {
			v, e := store.GetRouteTemplate(r.Context(), id)
			respondStore(w, v, e, 200)
		}
	case http.MethodPost, http.MethodPut:
		if (r.Method == http.MethodPost) != (id == "") {
			methodNotAllowed(w, "GET, PUT, DELETE")
			return
		}
		var in templateInput
		if !decode(w, r, &in) {
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		in.Description = strings.TrimSpace(strings.ReplaceAll(in.Description, "\r\n", "\n"))
		if utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 80 || utf8.RuneCountInString(in.Description) > 1000 || containsForbiddenControl(in.Name) || containsForbiddenControl(strings.ReplaceAll(strings.ReplaceAll(in.Description, "\n", ""), "\t", "")) {
			writeError(w, 400, "validation_error", "template name must be 1-80 characters and description at most 1000 characters")
			return
		}
		if id != "" && in.ExpectedVersion < 1 {
			writeError(w, 400, "validation_error", "expected_version is required")
			return
		}
		if in.Config != nil && (in.SourceNodeID != "" || in.SourceRouteID != "") {
			writeError(w, 400, "validation_error", "choose config or source route")
			return
		}
		var config routeInput
		if in.Config != nil {
			if in.Config.SchemaVersion > routeTemplateSchemaVersion {
				writeError(w, 400, "unsupported_template_schema", (&templateSchemaError{in.Config.SchemaVersion}).Error())
				return
			}
			config = in.Config.routeInput
		} else if validID(in.SourceNodeID) && validID(in.SourceRouteID) {
			route, err := a.store.GetRoute(r.Context(), in.SourceNodeID, in.SourceRouteID)
			if err != nil {
				respondStore(w, nil, err, 200)
				return
			}
			config, err = portableRouteInput(route)
			if err != nil {
				respondStore(w, nil, err, 200)
				return
			}
		} else {
			writeError(w, 400, "validation_error", "config or a valid source route is required")
			return
		}
		raw, err := validatedTemplateConfig(config)
		if err != nil {
			writeError(w, 400, "validation_error", err.Error())
			return
		}
		v, e := store.SaveRouteTemplate(r.Context(), id, in.Name, in.Description, raw, in.ExpectedVersion)
		if e == nil {
			addAuditDetail(r, auditExtraResourceID, v.ID)
			addAuditDetail(r, "template_version", v.Version)
		}
		status := 200
		if id == "" {
			status = 201
		}
		respondStore(w, v, e, status)
	case http.MethodDelete:
		if id == "" {
			methodNotAllowed(w, "GET, POST")
			return
		}
		var in struct {
			ExpectedVersion int64 `json:"expected_version"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.ExpectedVersion < 1 {
			writeError(w, 400, "validation_error", "expected_version is required")
			return
		}
		e := store.DeleteRouteTemplate(r.Context(), id, in.ExpectedVersion)
		if e == nil {
			w.WriteHeader(204)
		} else {
			respondStore(w, nil, e, 200)
		}
	default:
		methodNotAllowed(w, "GET, POST, PUT, DELETE")
	}
}

type routeCopyInput struct {
	NodeIDs         []string `json:"node_ids"`
	Name            string   `json:"name"`
	ExpectedVersion int64    `json:"expected_version"`
	// CopySuffix optionally localizes the same-node copy suffix (default " (копия)").
	CopySuffix string `json:"copy_suffix"`
}
type routeCopyResult struct {
	NodeID string `json:"node_id"`
	Route  *Route `json:"route,omitempty"`
	Error  string `json:"error,omitempty"`
	// ErrorCode is the stable machine-readable reason for Error (the UI
	// localizes by code and falls back to Error for unknown codes).
	ErrorCode string `json:"error_code,omitempty"`
}

func (a *API) copyRoute(w http.ResponseWriter, r *http.Request, nodeID, routeID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in routeCopyInput
	if !decode(w, r, &in) {
		return
	}
	route, err := a.store.GetRoute(r.Context(), nodeID, routeID)
	if err != nil {
		respondStore(w, nil, err, 200)
		return
	}
	config, err := portableRouteInput(route)
	if err != nil {
		respondStore(w, nil, err, 200)
		return
	}
	addAuditDetail(r, "node_ids", in.NodeIDs)
	a.createRouteCopies(w, r, config, in, nodeID)
}
func (a *API) applyRouteTemplate(w http.ResponseWriter, r *http.Request, store routeTemplateStore, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in routeCopyInput
	if !decode(w, r, &in) {
		return
	}
	template, err := store.GetRouteTemplate(r.Context(), id)
	if err != nil {
		respondStore(w, nil, err, 200)
		return
	}
	if in.ExpectedVersion != template.Version {
		respondStore(w, nil, ErrRouteVersionConflict, 200)
		return
	}
	config, err := templateRouteInput(template.Config)
	var schemaErr *templateSchemaError
	if errors.As(err, &schemaErr) {
		writeError(w, 400, "unsupported_template_schema", err.Error())
		return
	}
	if err != nil {
		respondStore(w, nil, err, 200)
		return
	}
	addAuditDetail(r, "node_ids", in.NodeIDs)
	addAuditDetail(r, "template_version", template.Version)
	a.createRouteCopies(w, r, config, in, "")
}

// routeCopySuffix marks an unnamed copy on the source node, so the copy and
// the original are distinguishable in the route list.
const routeCopySuffix = " (копия)"

// createRouteCopies creates disabled drafts on in.NodeIDs. sourceNodeID is
// the node of the copied route ("" for a template).
func (a *API) createRouteCopies(w http.ResponseWriter, r *http.Request, config routeInput, in routeCopyInput, sourceNodeID string) {
	if len(in.NodeIDs) < 1 || len(in.NodeIDs) > 100 {
		writeError(w, 400, "validation_error", "choose between 1 and 100 nodes")
		return
	}
	seen := map[string]bool{}
	for _, id := range in.NodeIDs {
		if !validID(id) || seen[id] {
			writeError(w, 400, "validation_error", "node ids must be valid and unique")
			return
		}
		seen[id] = true
	}
	if in.Name != "" {
		config.Name = in.Name
	}
	config.Enabled = new(bool)
	config.ExpectedVersion = nil
	spec, err := validateRoute(config, false)
	if err != nil {
		writeError(w, 400, "validation_error", err.Error())
		return
	}
	sameNodeSpec := spec
	if in.Name == "" && sourceNodeID != "" {
		suffix := routeCopySuffix
		if custom := strings.TrimRight(in.CopySuffix, " "); strings.TrimSpace(custom) != "" && utf8.RuneCountInString(custom) <= 20 && !containsForbiddenControl(custom) {
			suffix = custom
		}
		base := []rune(spec.Name)
		if limit := 80 - utf8.RuneCountInString(suffix); len(base) > limit {
			base = base[:limit]
		}
		sameNodeSpec.Name = strings.TrimRight(string(base), " ") + suffix
	}
	results := make([]routeCopyResult, 0, len(in.NodeIDs))
	for _, id := range in.NodeIDs {
		target := spec
		if id == sourceNodeID {
			target = sameNodeSpec
		}
		v, e := a.store.CreateRoute(r.Context(), id, target)
		result := routeCopyResult{NodeID: id}
		if e != nil {
			// Do not expose database statements or internal errors to the browser.
			result.Error, result.ErrorCode = "Не удалось создать черновик на ноде", "route_copy_failed"
			if errors.Is(e, ErrNotFound) {
				result.Error, result.ErrorCode = "Нода не найдена", "node_not_found"
			}
			if errors.Is(e, ErrAdvancedConfig) {
				// Same message and code respondStore returns for a single route.
				result.Error, result.ErrorCode = ErrAdvancedConfig.Error(), "advanced_config_active"
			}
			switch e.(type) {
			case *KernelShaperUnsupportedError:
				result.Error, result.ErrorCode = e.Error(), kernelShaperRequiresAgentCode
			case *LeastPingUnsupportedError:
				result.Error, result.ErrorCode = e.Error(), leastPingRequiresAgentCode
			case *IPWeightsUnsupportedError:
				result.Error, result.ErrorCode = e.Error(), ipWeightsRequiresAgentCode
			case *LeastConnToleranceUnsupportedError:
				result.Error, result.ErrorCode = e.Error(), leastConnToleranceRequiresAgentCode
			case *PPTrustedDomainsUnsupportedError:
				result.Error, result.ErrorCode = e.Error(), ppTrustedDomainsRequiresAgentCode
			}
		} else {
			result.Route = &v
		}
		results = append(results, result)
	}
	writeJSON(w, 200, results)
}
