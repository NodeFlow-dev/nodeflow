package panel

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTrafficSnapshotNormal(t *testing.T) {
	metrics := map[string]any{"haproxy_runtime": map[string]any{
		"counter_generation": "4312:1783930000:7",
		"bytes_in":           json.Number("1000"), "bytes_out": json.Number("2000"),
		"backends": map[string]any{
			RouteBackendKey(testRouteID): map[string]any{
				"bytes_in": json.Number("400"), "bytes_out": json.Number("600"),
			},
		},
	}}
	snapshot, present, err := parseTrafficSnapshot(metrics)
	require.NoError(t, err)
	require.True(t, present)
	assert.Equal(t, "4312:1783930000:7", snapshot.Generation)
	assert.Equal(t, trafficCounter{BytesIn: 1000, BytesOut: 2000}, snapshot.Node)
	assert.Equal(t, trafficCounter{BytesIn: 400, BytesOut: 600}, snapshot.Backends[RouteBackendKey(testRouteID)])
}

func TestParseTrafficSnapshotNoMetrics(t *testing.T) {
	for _, metrics := range []map[string]any{
		nil,
		{},
		{"load": json.Number("1")},
		{"haproxy_runtime": nil},
	} {
		snapshot, present, err := parseTrafficSnapshot(metrics)
		require.NoError(t, err)
		assert.False(t, present)
		assert.Empty(t, snapshot.Backends)
	}
}

func TestParseTrafficSnapshotBounds(t *testing.T) {
	tests := []map[string]any{
		{"haproxy_runtime": "bad"},
		{"haproxy_runtime": map[string]any{"bytes_in": -1, "bytes_out": 0}},
		{"haproxy_runtime": map[string]any{"bytes_in": 1.5, "bytes_out": 0}},
		{"haproxy_runtime": map[string]any{"bytes_in": uint64(math.MaxInt64) + 1, "bytes_out": 0}},
		{"haproxy_runtime": map[string]any{"counter_generation": "pid:start:reload", "bytes_in": 0, "bytes_out": 0}},
		{"haproxy_runtime": map[string]any{"counter_generation": 123, "bytes_in": 0, "bytes_out": 0}},
		{"haproxy_runtime": map[string]any{"bytes_in": 0, "bytes_out": 0, "backends": map[string]any{
			"bad\nname": map[string]any{"bytes_in": 0, "bytes_out": 0},
		}}},
	}
	for _, metrics := range tests {
		_, present, err := parseTrafficSnapshot(metrics)
		assert.True(t, present)
		assert.ErrorIs(t, err, ErrInvalidTrafficMetrics)
	}
}

func TestTrafficDeltaNormalResetAndDuplicate(t *testing.T) {
	previous := trafficCounter{BytesIn: 100, BytesOut: 200}
	assert.Equal(t, trafficCounter{BytesIn: 50, BytesOut: 60},
		nextTrafficDelta(&previous, trafficCounter{BytesIn: 150, BytesOut: 260}))
	assert.Equal(t, trafficCounter{BytesIn: 20, BytesOut: 30},
		nextTrafficDelta(&previous, trafficCounter{BytesIn: 20, BytesOut: 30}), "counter decrease is a reset")
	assert.Equal(t, trafficCounter{}, nextTrafficDelta(&previous, previous), "duplicate snapshot is idempotent")
	assert.Equal(t, previous, nextTrafficDelta(nil, previous), "first snapshot accounts traffic since HAProxy start")
}

func TestTrafficRateNormalFirstResetAndOrdering(t *testing.T) {
	previous := trafficCounter{BytesIn: 100, BytesOut: 200}
	previousAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	rate := rateBetweenTrafficCounters(&previous, &previousAt, trafficCounter{BytesIn: 150, BytesOut: 260}, previousAt.Add(10*time.Second))
	require.True(t, rate.Valid)
	assert.Equal(t, 5.0, rate.RXBytesPerSecond)
	assert.Equal(t, 6.0, rate.TXBytesPerSecond)

	assert.False(t, rateBetweenTrafficCounters(nil, nil, previous, previousAt).Valid, "the first counter is a baseline, not a rate")
	assert.False(t, rateBetweenTrafficCounters(&previous, &previousAt, trafficCounter{BytesIn: 10, BytesOut: 260}, previousAt.Add(10*time.Second)).Valid, "a counter reset must not create a spike")
	assert.False(t, rateBetweenTrafficCounters(&previous, &previousAt, trafficCounter{BytesIn: 150, BytesOut: 260}, previousAt).Valid, "non-increasing observation time is not a rate")

	duplicate := rateBetweenTrafficCounters(&previous, &previousAt, previous, previousAt.Add(10*time.Second))
	require.True(t, duplicate.Valid)
	assert.Zero(t, duplicate.RXBytesPerSecond)
	assert.Zero(t, duplicate.TXBytesPerSecond)
}

func TestTrafficCounterTransitionUsesHAProxyGeneration(t *testing.T) {
	previous := trafficCounter{BytesIn: 100, BytesOut: 200}
	previousAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	current := trafficCounter{BytesIn: 150, BytesOut: 260}

	delta, rate := trafficCounterTransition(&previous, &previousAt, "4312:1783930000:1", "4312:1783930000:1", current, previousAt.Add(10*time.Second))
	assert.Equal(t, trafficCounter{BytesIn: 50, BytesOut: 60}, delta)
	assert.True(t, rate.Valid)

	delta, rate = trafficCounterTransition(&previous, &previousAt, "4312:1783930000:1", "4312:1783930000:2", current, previousAt.Add(10*time.Second))
	assert.Equal(t, current, delta, "a new HAProxy generation starts a new cumulative counter even if it already surpassed the old raw value")
	assert.False(t, rate.Valid, "a generation boundary cannot produce an instantaneous rate")

	delta, rate = trafficCounterTransition(&previous, &previousAt, "", "4312:1783930000:2", current, previousAt.Add(10*time.Second))
	assert.Equal(t, trafficCounter{BytesIn: 50, BytesOut: 60}, delta, "establishing generation metadata must not double-count existing totals")
	assert.False(t, rate.Valid, "the metadata upgrade heartbeat is a new rate baseline")
}

func TestTrafficCounterTransitionIgnoresImmutableRevisionChange(t *testing.T) {
	revisionOne, revisionTwo := int64(1), int64(2)
	firstAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	first := trafficCounter{BytesIn: 100}
	second := trafficCounter{BytesIn: 120}
	third := trafficCounter{BytesIn: 130}

	sourceOne := trafficSourceGeneration("", &revisionOne)
	sourceTwo := trafficSourceGeneration("", &revisionTwo)
	deltaOne, _ := trafficCounterTransition(nil, nil, "", sourceOne, first, firstAt)
	deltaTwo, rateTwo := trafficCounterTransition(&first, &firstAt, sourceOne, sourceTwo, second, firstAt.Add(10*time.Second))
	secondAt := firstAt.Add(10 * time.Second)
	deltaThree, rateThree := trafficCounterTransition(&second, &secondAt, sourceTwo, sourceTwo, third, firstAt.Add(20*time.Second))

	assert.Equal(t, int64(130), deltaOne.BytesIn+deltaTwo.BytesIn+deltaThree.BytesIn,
		"rev1 100 -> rev2 120 -> 130 must account 130, not re-add the rev2 cumulative counter")
	assert.Equal(t, trafficCounter{BytesIn: 20}, deltaTwo)
	assert.True(t, rateTwo.Valid)
	assert.Equal(t, trafficCounter{BytesIn: 10}, deltaThree)
	assert.True(t, rateThree.Valid)

	sameHAProxyOne := trafficSourceGeneration("4312:1783930000:7", &revisionOne)
	sameHAProxyTwo := trafficSourceGeneration("4312:1783930000:7", &revisionTwo)
	delta, rate := trafficCounterTransition(&first, &firstAt, sameHAProxyOne, sameHAProxyTwo, second, secondAt)
	assert.Equal(t, trafficCounter{BytesIn: 20}, delta)
	assert.True(t, rate.Valid, "a config revision alone is not a HAProxy counter generation change")
}

func TestTrafficSourceGenerationIncludesAppliedRevision(t *testing.T) {
	revision := int64(42)
	assert.Equal(t, "4312:1783930000:7@revision:42", trafficSourceGeneration("4312:1783930000:7", &revision))
	assert.Equal(t, "revision:42", trafficSourceGeneration("", &revision))
	assert.Equal(t, "4312:1783930000:7", trafficSourceGeneration("4312:1783930000:7", nil))
}

func TestTrafficDeltaMonthBoundary(t *testing.T) {
	buckets := map[string]trafficCounter{}
	var previous *trafficCounter
	apply := func(at time.Time, current trafficCounter) {
		delta := nextTrafficDelta(previous, current)
		key := trafficMonth(at).Format("2006-01")
		bucket := buckets[key]
		bucket.BytesIn += delta.BytesIn
		bucket.BytesOut += delta.BytesOut
		buckets[key] = bucket
		copy := current
		previous = &copy
	}
	apply(time.Date(2026, 7, 31, 23, 59, 0, 0, time.UTC), trafficCounter{BytesIn: 100, BytesOut: 200})
	apply(time.Date(2026, 8, 1, 0, 1, 0, 0, time.UTC), trafficCounter{BytesIn: 130, BytesOut: 240})
	apply(time.Date(2026, 8, 1, 0, 2, 0, 0, time.UTC), trafficCounter{BytesIn: 130, BytesOut: 240})
	assert.Equal(t, trafficCounter{BytesIn: 100, BytesOut: 200}, buckets["2026-07"])
	assert.Equal(t, trafficCounter{BytesIn: 30, BytesOut: 40}, buckets["2026-08"])
}

func TestRouteBackendKeyContract(t *testing.T) {
	assert.Equal(t, "nf_be_222222222222", RouteBackendKey(testRouteID))
	assert.Equal(t,
		"nf_be_222222222222/nf_srv_222222222222",
		quotaRuntimeKey(testRouteID),
	)
}

func TestParseQuotaRuntime(t *testing.T) {
	key := quotaRuntimeKey(testRouteID)
	legacyKey := legacyRouteBackendKey(testRouteID) + "/" + legacyRouteServerKey(testRouteID)
	got, present, err := parseQuotaRuntime(map[string]any{
		"quota_runtime": map[string]any{key: true, legacyKey: false},
	})
	require.NoError(t, err)
	require.True(t, present)
	assert.Equal(t, map[string]bool{key: true, legacyKey: false}, got)

	for _, value := range []any{
		"bad",
		map[string]any{"bad/key": true},
		map[string]any{key: "true"},
		map[string]any{"nf_be_22222222222242228222222222222222/nf_srv_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": true},
	} {
		_, present, err = parseQuotaRuntime(map[string]any{"quota_runtime": value})
		assert.True(t, present)
		assert.ErrorIs(t, err, ErrInvalidTrafficMetrics)
	}
}

func TestParseTrafficMonth(t *testing.T) {
	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.FixedZone("test", 3*60*60))
	month, err := parseTrafficMonth("", now)
	require.NoError(t, err)
	assert.Equal(t, "2026-07", month.Format("2006-01"))
	_, err = parseTrafficMonth("2026-7", now)
	assert.Error(t, err)
}

func TestQuotaWindowUTCPeriods(t *testing.T) {
	anchor := time.Date(2026, 1, 15, 10, 20, 30, 0, time.UTC)
	at := time.Date(2026, 7, 12, 15, 45, 20, 0, time.FixedZone("UTC+3", 3*60*60))
	tests := []struct {
		period string
		start  time.Time
		end    time.Time
	}{
		{"hourly", time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC), time.Date(2026, 7, 12, 13, 0, 0, 0, time.UTC)},
		{"daily", time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)},
		{"calendar_month", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		t.Run(test.period, func(t *testing.T) {
			window, err := quotaWindowAt(test.period, anchor, at)
			require.NoError(t, err)
			assert.Equal(t, test.start, window.Start)
			assert.Equal(t, test.end, window.End)
		})
	}
}

func TestSplitQuotaDeltaAcrossUTCWindowBoundaries(t *testing.T) {
	anchor := time.Date(2025, 1, 31, 10, 15, 30, 0, time.UTC)
	tests := []struct {
		period   string
		boundary time.Time
	}{
		{"hourly", time.Date(2025, 2, 28, 11, 0, 0, 0, time.UTC)},
		{"daily", time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"calendar_month", time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"monthly_from_creation", time.Date(2025, 2, 28, 10, 15, 30, 0, time.UTC)},
	}
	for _, test := range tests {
		t.Run(test.period, func(t *testing.T) {
			parts, err := splitQuotaDelta(
				test.period,
				anchor,
				test.boundary.Add(-10*time.Second),
				test.boundary.Add(10*time.Second),
				trafficCounter{BytesIn: 101, BytesOut: 59},
			)
			require.NoError(t, err)
			require.Len(t, parts, 2)
			assert.Equal(t, test.boundary, parts[0].Window.End)
			assert.Equal(t, test.boundary, parts[1].Window.Start)
			assert.Equal(t, trafficCounter{BytesIn: 50, BytesOut: 29}, parts[0].Delta)
			assert.Equal(t, trafficCounter{BytesIn: 51, BytesOut: 30}, parts[1].Delta)
			assert.Equal(t, int64(101), parts[0].Delta.BytesIn+parts[1].Delta.BytesIn)
			assert.Equal(t, int64(59), parts[0].Delta.BytesOut+parts[1].Delta.BytesOut)

			endingAtBoundary, err := splitQuotaDelta(
				test.period, anchor, test.boundary.Add(-10*time.Second), test.boundary,
				trafficCounter{BytesIn: 7},
			)
			require.NoError(t, err)
			require.Len(t, endingAtBoundary, 1, "[start,end) must keep an interval ending at the boundary in the old window")
			assert.Equal(t, test.boundary, endingAtBoundary[0].Window.End)

			startingAtBoundary, err := splitQuotaDelta(
				test.period, anchor, test.boundary, test.boundary.Add(10*time.Second),
				trafficCounter{BytesIn: 7},
			)
			require.NoError(t, err)
			require.Len(t, startingAtBoundary, 1, "[start,end) must place an interval starting at the boundary in the new window")
			assert.Equal(t, test.boundary, startingAtBoundary[0].Window.Start)
		})
	}
}

func TestSplitQuotaDeltaPreservesMaxInt64Exactly(t *testing.T) {
	boundary := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	parts, err := splitQuotaDelta(
		"hourly", time.Time{}, boundary.Add(-time.Second), boundary.Add(time.Second),
		trafficCounter{BytesIn: math.MaxInt64, BytesOut: math.MaxInt64},
	)
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.Equal(t, int64(math.MaxInt64), parts[0].Delta.BytesIn+parts[1].Delta.BytesIn)
	assert.Equal(t, int64(math.MaxInt64), parts[0].Delta.BytesOut+parts[1].Delta.BytesOut)
}

func TestCanSplitQuotaDeltaOnlyForKnownMonotonicInterval(t *testing.T) {
	previous := trafficCounter{BytesIn: 100, BytesOut: 200}
	current := trafficCounter{BytesIn: 150, BytesOut: 250}
	previousAt := time.Date(2026, 7, 13, 11, 59, 50, 0, time.UTC)
	observedAt := previousAt.Add(20 * time.Second)

	assert.True(t, canSplitQuotaDelta(
		&previous, &previousAt,
		"4312:1783930000:1@revision:1", "4312:1783930000:1@revision:2",
		current, observedAt,
	), "an immutable revision change alone keeps the interval measurable")
	assert.True(t, canSplitQuotaDelta(
		&previous, &previousAt, "revision:1", "4312:1783930000:1@revision:2", current, observedAt,
	), "establishing generation metadata is not a real counter reset")
	assert.False(t, canSplitQuotaDelta(nil, nil, "", "", current, observedAt), "first sample has no interval")
	assert.False(t, canSplitQuotaDelta(
		&previous, &previousAt,
		"4312:1783930000:1", "4313:1783930010:1",
		current, observedAt,
	), "a real HAProxy generation change has no measurable counter interval")
	assert.False(t, canSplitQuotaDelta(
		&previous, &previousAt, "", "", trafficCounter{BytesIn: 10, BytesOut: 250}, observedAt,
	), "a raw counter reset has no measurable interval")
	assert.False(t, canSplitQuotaDelta(&previous, &previousAt, "", "", current, previousAt))
}

func TestAnniversaryQuotaWindowClampsFromOriginalDay(t *testing.T) {
	anchor := time.Date(2025, 1, 31, 10, 15, 30, 123, time.UTC)
	tests := []struct {
		name  string
		at    time.Time
		start time.Time
		end   time.Time
	}{
		{
			name:  "before February boundary",
			at:    time.Date(2025, 2, 28, 10, 15, 29, 0, time.UTC),
			start: anchor,
			end:   time.Date(2025, 2, 28, 10, 15, 30, 123, time.UTC),
		},
		{
			name:  "February boundary opens interval ending March 31",
			at:    time.Date(2025, 2, 28, 10, 15, 30, 123, time.UTC),
			start: time.Date(2025, 2, 28, 10, 15, 30, 123, time.UTC),
			end:   time.Date(2025, 3, 31, 10, 15, 30, 123, time.UTC),
		},
		{
			name:  "March boundary returns to day 31",
			at:    time.Date(2025, 3, 31, 10, 15, 30, 123, time.UTC),
			start: time.Date(2025, 3, 31, 10, 15, 30, 123, time.UTC),
			end:   time.Date(2025, 4, 30, 10, 15, 30, 123, time.UTC),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window, err := quotaWindowAt("monthly_from_creation", anchor, test.at)
			require.NoError(t, err)
			assert.Equal(t, test.start, window.Start)
			assert.Equal(t, test.end, window.End)
		})
	}

	leapAnchor := time.Date(2024, 1, 31, 8, 0, 0, 0, time.UTC)
	leap, err := quotaWindowAt("monthly_from_creation", leapAnchor, time.Date(2024, 2, 29, 8, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, time.Date(2024, 2, 29, 8, 0, 0, 0, time.UTC), leap.Start)
	assert.Equal(t, time.Date(2024, 3, 31, 8, 0, 0, 0, time.UTC), leap.End)
}

func TestQuotaWindowRejectsMissingAnchorAndUnknownPeriod(t *testing.T) {
	_, err := quotaWindowAt("monthly_from_creation", time.Time{}, time.Now())
	assert.Error(t, err)
	_, err = quotaWindowAt("weekly", time.Now(), time.Now())
	assert.Error(t, err)
}
