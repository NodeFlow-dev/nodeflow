package panel

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHeartbeatTrafficSampleOrdering(t *testing.T) {
	firstID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	secondID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	firstStart := time.Date(2026, 7, 13, 1, 2, 3, 4000, time.UTC)
	secondStart := firstStart.Add(time.Second)
	storedSeq := int64(2)
	stored := heartbeatTrafficOrderState{InstanceID: &firstID, StartedAt: &firstStart, SampleSeq: &storedSeq}

	assert.False(t, heartbeatTrafficSampleNewer(stored, orderedHeartbeat(firstID, firstStart, 1)), "out-of-order same-instance sample")
	assert.False(t, heartbeatTrafficSampleNewer(stored, orderedHeartbeat(firstID, firstStart, 2)), "duplicate same-instance sample")
	assert.True(t, heartbeatTrafficSampleNewer(stored, orderedHeartbeat(firstID, firstStart, 3)), "next same-instance sample")
	assert.True(t, heartbeatTrafficSampleNewer(stored, orderedHeartbeat(secondID, secondStart, 1)), "newer process instance")
	assert.False(t, heartbeatTrafficSampleNewer(stored, orderedHeartbeat(secondID, firstStart.Add(-time.Second), 99)), "delayed older process instance")
	assert.False(t, heartbeatTrafficSampleNewer(stored, Heartbeat{}), "legacy sample after ordered Agent")
	assert.True(t, heartbeatTrafficSampleNewer(heartbeatTrafficOrderState{}, Heartbeat{}), "legacy Agent remains compatible")
	assert.True(t, heartbeatTrafficSampleNewer(heartbeatTrafficOrderState{}, orderedHeartbeat(firstID, firstStart, 1)), "legacy Agent can upgrade")
}

func TestHeartbeatTrafficOrderValidation(t *testing.T) {
	valid := orderedHeartbeat("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", time.Now().UTC().Truncate(time.Microsecond), 1)
	assert.NoError(t, validateHeartbeatTrafficOrder(Heartbeat{}))
	assert.NoError(t, validateHeartbeatTrafficOrder(valid))

	partial := valid
	partial.TrafficSampleSeq = nil
	assert.ErrorIs(t, validateHeartbeatTrafficOrder(partial), ErrInvalidHeartbeatOrder)
	badID := valid
	badID.TrafficInstanceID = "not-a-uuid"
	assert.ErrorIs(t, validateHeartbeatTrafficOrder(badID), ErrInvalidHeartbeatOrder)
	badSeq := valid
	zero := int64(0)
	badSeq.TrafficSampleSeq = &zero
	assert.ErrorIs(t, validateHeartbeatTrafficOrder(badSeq), ErrInvalidHeartbeatOrder)
	badZone := valid
	started := time.Date(2026, 7, 13, 4, 0, 0, 0, time.FixedZone("UTC+3", 3*60*60))
	badZone.TrafficInstanceStartedAt = &started
	assert.ErrorIs(t, validateHeartbeatTrafficOrder(badZone), ErrInvalidHeartbeatOrder)
}

func orderedHeartbeat(instanceID string, startedAt time.Time, sequence int64) Heartbeat {
	startedAt = startedAt.UTC().Truncate(time.Microsecond)
	return Heartbeat{
		TrafficInstanceID: instanceID, TrafficInstanceStartedAt: &startedAt, TrafficSampleSeq: &sequence,
	}
}
