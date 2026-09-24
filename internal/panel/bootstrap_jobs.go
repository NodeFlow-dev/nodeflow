package panel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/nodeflow/nodeflow/internal/bootstrap"
)

type bootstrapJobStatus string

const (
	bootstrapJobQueued    bootstrapJobStatus = "queued"
	bootstrapJobRunning   bootstrapJobStatus = "running"
	bootstrapJobInstalled bootstrapJobStatus = "installed"
	bootstrapJobFailed    bootstrapJobStatus = "failed"

	defaultBootstrapJobLimit       = 32
	defaultBootstrapJobConcurrency = 2
	defaultBootstrapJobTTL         = 15 * time.Minute
	defaultBootstrapRunTimeout     = 15 * time.Minute
	maxBootstrapJournalEntries     = 32
)

var errBootstrapJobLimit = errors.New("bootstrap job limit reached")

// bootstrapJob contains only polling-safe state. SSH credentials are held by
// the bounded goroutine closure and are cleared before that goroutine exits.
type bootstrapJob struct {
	ID             string
	Key            string
	Status         bootstrapJobStatus
	Stage          string
	NodeID         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      time.Time
	Journal        []bootstrapJobJournalEntry
	FailureSummary string
	FailureCode    string
	ExitCode       int
}

// bootstrapJobJournalEntry is deliberately limited to allow-listed stage
// identifiers and server timestamps. Installer stdout/stderr and credentials
// must never enter polling-safe job state.
type bootstrapJobJournalEntry struct {
	Stage  string             `json:"stage"`
	Status bootstrapJobStatus `json:"status"`
	At     time.Time          `json:"at"`
}

type bootstrapJobView struct {
	JobID          string                     `json:"job_id"`
	Status         bootstrapJobStatus         `json:"status"`
	Stage          string                     `json:"stage"`
	NodeID         string                     `json:"node_id,omitempty"`
	CreatedAt      time.Time                  `json:"created_at"`
	UpdatedAt      time.Time                  `json:"updated_at"`
	Journal        []bootstrapJobJournalEntry `json:"journal"`
	FailureSummary string                     `json:"failure_summary,omitempty"`
	FailureCode    string                     `json:"failure_code,omitempty"`
	ExitCode       int                        `json:"exit_code,omitempty"`
}

type bootstrapJobOutcome struct {
	NodeID         string
	Stage          string
	DiagnosticCode string
	ExitCode       int
}

type bootstrapJobRunner func(context.Context, *bootstrap.Request) bootstrapJobOutcome
type bootstrapJobIDContextKey struct{}

type bootstrapJobStore struct {
	mu         sync.Mutex
	jobs       map[string]*bootstrapJob
	activeKeys map[string]string
	sem        chan struct{}
	limit      int
	ttl        time.Duration
	runTimeout time.Duration
	now        func() time.Time
}

func newBootstrapJobStore(limit, concurrency int, ttl, runTimeout time.Duration) *bootstrapJobStore {
	if limit < 1 {
		limit = defaultBootstrapJobLimit
	}
	if concurrency < 1 {
		concurrency = defaultBootstrapJobConcurrency
	}
	if concurrency > limit {
		concurrency = limit
	}
	if ttl <= 0 {
		ttl = defaultBootstrapJobTTL
	}
	if runTimeout <= 0 {
		runTimeout = defaultBootstrapRunTimeout
	}
	return &bootstrapJobStore{
		jobs:       make(map[string]*bootstrapJob),
		activeKeys: make(map[string]string),
		sem:        make(chan struct{}, concurrency),
		limit:      limit,
		ttl:        ttl,
		runTimeout: runTimeout,
		now:        time.Now,
	}
}

func (s *bootstrapJobStore) submit(request bootstrap.Request, runner bootstrapJobRunner) (bootstrapJobView, error) {
	return s.submitKeyed("", request, runner)
}

// submitKeyed coalesces concurrent operations for the same resource. The
// duplicate request's secrets are cleared immediately and the active job is
// returned, so it never consumes another global installer slot.
func (s *bootstrapJobStore) submitKeyed(key string, request bootstrap.Request, runner bootstrapJobRunner) (bootstrapJobView, error) {
	now := s.now()
	s.mu.Lock()
	s.pruneExpiredLocked(now)
	if key != "" {
		if activeID, ok := s.activeKeys[key]; ok {
			if activeJob, found := s.jobs[activeID]; found {
				view := activeJob.view()
				s.mu.Unlock()
				request.ClearSecrets()
				return view, nil
			}
			delete(s.activeKeys, key)
		}
	}
	if len(s.jobs) >= s.limit {
		s.mu.Unlock()
		request.ClearSecrets()
		return bootstrapJobView{}, errBootstrapJobLimit
	}
	id, err := newBootstrapJobID()
	if err != nil {
		s.mu.Unlock()
		request.ClearSecrets()
		return bootstrapJobView{}, err
	}
	job := &bootstrapJob{
		ID: id, Key: key, Status: bootstrapJobQueued, Stage: "queued", CreatedAt: now, UpdatedAt: now,
		Journal: []bootstrapJobJournalEntry{{Stage: "queued", Status: bootstrapJobQueued, At: now}},
	}
	s.jobs[id] = job
	if key != "" {
		s.activeKeys[key] = id
	}
	view := job.view()
	s.mu.Unlock()

	go s.run(id, request, runner)
	return view, nil
}

func (s *bootstrapJobStore) run(id string, request bootstrap.Request, runner bootstrapJobRunner) {
	terminal := false
	defer func() {
		panicked := recover() != nil
		if !terminal {
			request.ClearSecrets()
		}
		if panicked && !terminal {
			s.update(id, bootstrapJobFailed, "install", "")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), s.runTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, bootstrapJobIDContextKey{}, id)
	ctx = bootstrap.WithProgress(ctx, func(stage string) {
		s.update(id, bootstrapJobRunning, safeBootstrapStage(stage), "")
	})
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		request.ClearSecrets()
		s.update(id, bootstrapJobFailed, "timeout", "")
		terminal = true
		return
	}
	s.update(id, bootstrapJobRunning, "installing", "")
	outcome := runner(ctx, &request)
	// Publish a terminal state only after the in-memory credential copy has
	// been scrubbed. A client observing installed/failed can therefore never
	// race the retained job closure's cleanup.
	request.ClearSecrets()
	if outcome.NodeID != "" {
		s.update(id, bootstrapJobInstalled, "installed", outcome.NodeID)
		terminal = true
		return
	}
	s.updateFailure(id, outcome.Stage, outcome.DiagnosticCode, outcome.ExitCode)
	terminal = true
}

func bootstrapJobID(ctx context.Context) string {
	id, _ := ctx.Value(bootstrapJobIDContextKey{}).(string)
	return id
}

func (s *bootstrapJobStore) get(id string) (bootstrapJobView, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	job, ok := s.jobs[id]
	if !ok {
		return bootstrapJobView{}, false
	}
	return job.view(), true
}

func (s *bootstrapJobStore) update(id string, status bootstrapJobStatus, stage, nodeID string) {
	now := s.now()
	stage = safeBootstrapStage(stage)
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	job.Status, job.Stage, job.UpdatedAt = status, stage, now
	job.appendJournal(status, stage, now)
	if status == bootstrapJobInstalled {
		job.NodeID = nodeID
		job.FailureSummary = ""
		job.FailureCode = ""
		job.ExitCode = 0
	} else {
		job.NodeID = ""
	}
	if status == bootstrapJobFailed {
		job.FailureSummary = safeBootstrapFailureSummary(stage, "")
	}
	if status == bootstrapJobInstalled || status == bootstrapJobFailed {
		job.ExpiresAt = now.Add(s.ttl)
		if job.Key != "" && s.activeKeys[job.Key] == id {
			delete(s.activeKeys, job.Key)
		}
	}
}

func (s *bootstrapJobStore) updateFailure(id, stage, diagnostic string, exitCode int) {
	s.update(id, bootstrapJobFailed, stage, "")
	diagnostic = safeBootstrapDiagnostic(diagnostic)
	if exitCode < 1 || exitCode > 255 {
		exitCode = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	job.FailureCode = diagnostic
	job.ExitCode = exitCode
	job.FailureSummary = safeBootstrapFailureSummary(job.Stage, diagnostic)
}

func (s *bootstrapJobStore) pruneExpiredLocked(now time.Time) {
	for id, job := range s.jobs {
		if !job.ExpiresAt.IsZero() && !job.ExpiresAt.After(now) {
			if job.Key != "" && s.activeKeys[job.Key] == id {
				delete(s.activeKeys, job.Key)
			}
			delete(s.jobs, id)
		}
	}
}

func (j *bootstrapJob) view() bootstrapJobView {
	journal := append([]bootstrapJobJournalEntry(nil), j.Journal...)
	return bootstrapJobView{
		JobID: j.ID, Status: j.Status, Stage: j.Stage, NodeID: j.NodeID,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, Journal: journal,
		FailureSummary: j.FailureSummary, FailureCode: j.FailureCode, ExitCode: j.ExitCode,
	}
}

func (j *bootstrapJob) appendJournal(status bootstrapJobStatus, stage string, at time.Time) {
	if count := len(j.Journal); count > 0 {
		last := j.Journal[count-1]
		if last.Status == status && last.Stage == stage {
			return
		}
	}
	j.Journal = append(j.Journal, bootstrapJobJournalEntry{Stage: stage, Status: status, At: at})
	if len(j.Journal) > maxBootstrapJournalEntries {
		j.Journal = append([]bootstrapJobJournalEntry(nil), j.Journal[len(j.Journal)-maxBootstrapJournalEntries:]...)
	}
}

func newBootstrapJobID() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func safeBootstrapStage(stage string) string {
	switch stage {
	case "queued", "installing", "installed", "configuration", "updater_binary", "identity", "authentication", "connect", "preflight", "release", "release_assignment", "prepare", "upload", "updater_upload", "privilege", "install", "create_node", "lookup_node", "generate_token", "store_token", "verify_credential", "revoke_credentials", "credential_cleanup", "rollback", "finalize", "firewall_policy", "timeout":
		return stage
	default:
		return "install"
	}
}

func safeBootstrapDiagnostic(diagnostic string) string {
	switch diagnostic {
	case "remote_command_failed", "remote_install_failed", "haproxy_release_unavailable", "haproxy_dependency_prepare_failed", "haproxy_candidate_libraries_missing", "haproxy_candidate_runtime_failed", "haproxy_config_validation_failed":
		return diagnostic
	default:
		return ""
	}
}

func safeBootstrapFailureSummary(stage, diagnostic string) string {
	switch safeBootstrapDiagnostic(diagnostic) {
	case "haproxy_release_unavailable":
		return "В официальном репозитории HAProxy нет подходящего пакета для этой версии Ubuntu."
	case "haproxy_dependency_prepare_failed":
		return "Не удалось подготовить зависимости для проверки HAProxy-кандидата."
	case "haproxy_candidate_libraries_missing":
		return "Для запуска HAProxy-кандидата не удалось собрать комплект runtime-библиотек."
	case "haproxy_candidate_runtime_failed":
		return "HAProxy-кандидат не запустился с подготовленными runtime-библиотеками."
	case "haproxy_config_validation_failed":
		return "Новая версия HAProxy не прошла проверку текущей конфигурации."
	case "remote_install_failed":
		return "Удалённый установочный скрипт завершился ошибкой."
	}
	switch safeBootstrapStage(stage) {
	case "authentication":
		return "SSH-аутентификация не пройдена. Проверьте пользователя и способ входа."
	case "connect":
		return "Панель не смогла установить SSH-соединение с нодой."
	case "privilege":
		return "Для установки недоступны необходимые права root или sudo."
	case "release", "release_assignment":
		return "Не найден или не назначен совместимый подписанный релиз Node Agent."
	case "timeout":
		return "Установка превысила допустимое время выполнения."
	case "upload", "updater_upload":
		return "Не удалось безопасно загрузить компоненты Node Agent на ноду."
	case "verify_credential":
		return "После установки нода не подтвердила новый защищённый канал связи."
	case "rollback":
		return "Не удалось автоматически восстановить прежние учётные данные ноды."
	default:
		return "Этап установки завершился ошибкой. Секреты и полный вывод команд в журнале не сохраняются."
	}
}
