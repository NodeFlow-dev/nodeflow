package panel

import (
	"context"
	"testing"
	"time"

	"github.com/nodeflow/nodeflow/internal/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBootstrapJobStoreScrubsSecretsBeforeTerminalPoll(t *testing.T) {
	jobs := newBootstrapJobStore(2, 1, time.Minute, time.Minute)
	request := bootstrap.Request{
		Password: "ssh-secret", PrivateKey: "private-secret",
		PrivateKeyPassphrase: "key-secret", SudoPassword: "sudo-secret",
		EnrollmentToken: "enrollment-secret",
	}
	var captured *bootstrap.Request
	created, err := jobs.submit(request, func(_ context.Context, in *bootstrap.Request) bootstrapJobOutcome {
		captured = in
		return bootstrapJobOutcome{NodeID: testNodeID, Stage: "installed"}
	})
	require.NoError(t, err)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job, ok := jobs.get(created.JobID)
		require.True(t, ok)
		if job.Status == bootstrapJobInstalled {
			require.NotNil(t, captured)
			assert.Empty(t, captured.Password)
			assert.Empty(t, captured.PrivateKey)
			assert.Empty(t, captured.PrivateKeyPassphrase)
			assert.Empty(t, captured.SudoPassword)
			assert.Empty(t, captured.EnrollmentToken)
			assert.Equal(t, testNodeID, job.NodeID)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("bootstrap job did not finish")
}

func TestBootstrapJobStoreLimitAndTTL(t *testing.T) {
	jobs := newBootstrapJobStore(1, 1, time.Second, time.Minute)
	release := make(chan struct{})
	created, err := jobs.submit(bootstrap.Request{Password: "first-secret"}, func(context.Context, *bootstrap.Request) bootstrapJobOutcome {
		<-release
		return bootstrapJobOutcome{Stage: "connect"}
	})
	require.NoError(t, err)

	_, err = jobs.submit(bootstrap.Request{Password: "second-secret"}, func(context.Context, *bootstrap.Request) bootstrapJobOutcome {
		return bootstrapJobOutcome{}
	})
	assert.ErrorIs(t, err, errBootstrapJobLimit)

	close(release)
	deadline := time.Now().Add(time.Second)
	terminal := false
	for time.Now().Before(deadline) {
		job, ok := jobs.get(created.JobID)
		require.True(t, ok)
		if job.Status == bootstrapJobFailed {
			terminal = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	require.True(t, terminal, "bootstrap job did not reach terminal state")

	jobs.mu.Lock()
	jobs.jobs[created.JobID].ExpiresAt = jobs.now().Add(-time.Nanosecond)
	jobs.mu.Unlock()
	_, ok := jobs.get(created.JobID)
	assert.False(t, ok)

	_, err = jobs.submit(bootstrap.Request{}, func(context.Context, *bootstrap.Request) bootstrapJobOutcome {
		return bootstrapJobOutcome{Stage: "install"}
	})
	assert.NoError(t, err)
}

func TestBootstrapJobStoreDuplicatePollingIsStable(t *testing.T) {
	jobs := newBootstrapJobStore(1, 1, time.Minute, time.Minute)
	created, err := jobs.submit(bootstrap.Request{}, func(context.Context, *bootstrap.Request) bootstrapJobOutcome {
		return bootstrapJobOutcome{NodeID: testNodeID}
	})
	require.NoError(t, err)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		first, ok := jobs.get(created.JobID)
		require.True(t, ok)
		if first.Status == bootstrapJobInstalled {
			second, ok := jobs.get(created.JobID)
			require.True(t, ok)
			assert.Equal(t, first, second)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("bootstrap job did not finish")
}

func TestBootstrapJobStoreCoalescesActiveKeyWithoutConsumingAnotherSlot(t *testing.T) {
	jobs := newBootstrapJobStore(2, 1, time.Minute, time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	first, err := jobs.submitKeyed("reinstall:"+testNodeID, bootstrap.Request{Password: "first-secret"}, func(context.Context, *bootstrap.Request) bootstrapJobOutcome {
		close(started)
		<-release
		return bootstrapJobOutcome{NodeID: testNodeID}
	})
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("keyed bootstrap job did not start")
	}

	duplicateRunnerCalled := false
	duplicate, err := jobs.submitKeyed("reinstall:"+testNodeID, bootstrap.Request{Password: "second-secret"}, func(context.Context, *bootstrap.Request) bootstrapJobOutcome {
		duplicateRunnerCalled = true
		return bootstrapJobOutcome{}
	})
	require.NoError(t, err)
	assert.Equal(t, first.JobID, duplicate.JobID)
	assert.False(t, duplicateRunnerCalled)

	close(release)
	deadline := time.Now().Add(time.Second)
	terminal := false
	for time.Now().Before(deadline) {
		job, ok := jobs.get(first.JobID)
		require.True(t, ok)
		if job.Status == bootstrapJobInstalled {
			terminal = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	require.True(t, terminal, "keyed bootstrap job did not finish")
	third, err := jobs.submitKeyed("reinstall:"+testNodeID, bootstrap.Request{}, func(context.Context, *bootstrap.Request) bootstrapJobOutcome {
		return bootstrapJobOutcome{Stage: "connect"}
	})
	require.NoError(t, err)
	assert.NotEqual(t, first.JobID, third.JobID)
}

func TestBootstrapJobStorePublishesSafeLiveStage(t *testing.T) {
	jobs := newBootstrapJobStore(1, 1, time.Minute, time.Minute)
	reported := make(chan struct{})
	release := make(chan struct{})
	created, err := jobs.submit(bootstrap.Request{}, func(ctx context.Context, _ *bootstrap.Request) bootstrapJobOutcome {
		bootstrap.ReportProgress(ctx, "connect")
		close(reported)
		<-release
		return bootstrapJobOutcome{NodeID: testNodeID}
	})
	require.NoError(t, err)
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("bootstrap runner did not publish progress")
	}
	job, ok := jobs.get(created.JobID)
	require.True(t, ok)
	assert.Equal(t, bootstrapJobRunning, job.Status)
	assert.Equal(t, "connect", job.Stage)
	close(release)
}

func TestSafeBootstrapStageDoesNotEchoUnknownInput(t *testing.T) {
	assert.Equal(t, "connect", safeBootstrapStage("connect"))
	assert.Equal(t, "install", safeBootstrapStage("ssh-secret"))
}

func TestBootstrapJobJournalIsBoundedAndPollingSafe(t *testing.T) {
	jobs := newBootstrapJobStore(1, 1, time.Minute, time.Minute)
	now := time.Now()
	job := &bootstrapJob{
		ID: "job", Status: bootstrapJobQueued, Stage: "queued", CreatedAt: now, UpdatedAt: now,
		Journal: []bootstrapJobJournalEntry{{Stage: "queued", Status: bootstrapJobQueued, At: now}},
	}
	jobs.jobs[job.ID] = job

	for index := 0; index < maxBootstrapJournalEntries+8; index++ {
		stage := "connect"
		if index%2 == 0 {
			stage = "prepare"
		}
		jobs.update(job.ID, bootstrapJobRunning, stage, "")
	}
	jobs.updateFailure(job.ID, "ssh-password=do-not-expose", "password=do-not-expose", 999)

	view, ok := jobs.get(job.ID)
	require.True(t, ok)
	require.LessOrEqual(t, len(view.Journal), maxBootstrapJournalEntries)
	assert.Equal(t, "install", view.Stage)
	assert.NotContains(t, view.FailureSummary, "do-not-expose")
	assert.Empty(t, view.FailureCode)
	assert.Zero(t, view.ExitCode)
	for _, entry := range view.Journal {
		assert.NotContains(t, entry.Stage, "do-not-expose")
	}

	copyView := view
	copyView.Journal[0].Stage = "mutated"
	stableView, ok := jobs.get(job.ID)
	require.True(t, ok)
	assert.NotEqual(t, "mutated", stableView.Journal[0].Stage)
}

func TestBootstrapJobFailurePublishesAllowListedDiagnostic(t *testing.T) {
	jobs := newBootstrapJobStore(1, 1, time.Minute, time.Minute)
	now := time.Now()
	jobs.jobs["job"] = &bootstrapJob{ID: "job", Status: bootstrapJobRunning, Stage: "install", CreatedAt: now, UpdatedAt: now}

	jobs.updateFailure("job", "install", "haproxy_candidate_runtime_failed", 74)
	view, ok := jobs.get("job")
	require.True(t, ok)
	assert.Equal(t, bootstrapJobFailed, view.Status)
	assert.Equal(t, "haproxy_candidate_runtime_failed", view.FailureCode)
	assert.Equal(t, 74, view.ExitCode)
	assert.Contains(t, view.FailureSummary, "runtime-библиотеками")
}

func TestBootstrapFailureSummaryIsStaticAndSanitized(t *testing.T) {
	assert.Equal(t, "SSH-аутентификация не пройдена. Проверьте пользователя и способ входа.", safeBootstrapFailureSummary("authentication", ""))
	assert.NotContains(t, safeBootstrapFailureSummary("token=super-secret", "password=super-secret"), "super-secret")
	assert.Equal(t, "HAProxy-кандидат не запустился с подготовленными runtime-библиотеками.", safeBootstrapFailureSummary("install", "haproxy_candidate_runtime_failed"))
}
