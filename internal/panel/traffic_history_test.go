package panel

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeartbeatRateSamplesDoNotDeleteOnCriticalPath(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	sourceFile := filepath.Join(filepath.Dir(testFile), "traffic_history.go")
	source, err := os.ReadFile(sourceFile)
	require.NoError(t, err)

	parsed, err := parser.ParseFile(token.NewFileSet(), sourceFile, source, 0)
	require.NoError(t, err)

	found := false
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "recordHAProxyRateSamples" {
			continue
		}
		found = true
		ast.Inspect(function.Body, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			query, err := strconv.Unquote(literal.Value)
			if err == nil {
				upper := strings.ToUpper(query)
				assert.NotContains(t, upper, "DELETE FROM", "retention DELETEs belong to RunDataRetentionCleaner, not the heartbeat transaction")
				assert.NotContains(t, upper, "TRAFFIC_RATE_RETENTION_STATE", "the retention gate belongs to RunDataRetentionCleaner")
			}
			return true
		})
	}
	require.True(t, found, "recordHAProxyRateSamples not found")
}

func TestTrafficHistoryRanges(t *testing.T) {
	tests := map[string]struct {
		window time.Duration
		bucket time.Duration
	}{
		"1m":  {time.Minute, 15 * time.Second},
		"5m":  {5 * time.Minute, 30 * time.Second},
		"1h":  {time.Hour, time.Minute},
		"24h": {24 * time.Hour, 5 * time.Minute},
		"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
		"30d": {30 * 24 * time.Hour, 2 * time.Hour},
	}
	for value, expected := range tests {
		t.Run(value, func(t *testing.T) {
			spec, ok := parseTrafficHistoryRange(value)
			require.True(t, ok)
			assert.Equal(t, expected.window, spec.window)
			assert.Equal(t, expected.bucket, spec.bucket)
		})
	}
	_, ok := parseTrafficHistoryRange("1d")
	assert.False(t, ok)
}

func float64Ref(value float64) *float64 {
	return &value
}

func TestFillTrafficHistoryGapsPreservesRealZeroAndEncodesMissingAsNull(t *testing.T) {
	spec := trafficHistoryRanges["5m"]
	observedAt := time.Date(2026, 7, 13, 12, 5, 17, 0, time.UTC)
	firstBucket := observedAt.Add(-spec.window).Truncate(spec.bucket)
	lastBucket := observedAt.Truncate(spec.bucket)

	samples := fillTrafficHistoryGaps(spec, observedAt, []TrafficHistorySample{
		{Timestamp: firstBucket.Add(4 * time.Second), RXBPS: float64Ref(0), TXBPS: float64Ref(0)},
		{Timestamp: lastBucket, RXBPS: float64Ref(80), TXBPS: float64Ref(40)},
	})

	require.Len(t, samples, 11)
	assert.Equal(t, firstBucket, samples[0].Timestamp)
	require.NotNil(t, samples[0].RXBPS)
	require.NotNil(t, samples[0].TXBPS)
	assert.Zero(t, *samples[0].RXBPS, "an observed zero rate must not become a gap")
	assert.Zero(t, *samples[0].TXBPS, "an observed zero rate must not become a gap")
	for _, sample := range samples[1:10] {
		assert.Nil(t, sample.RXBPS)
		assert.Nil(t, sample.TXBPS)
	}
	require.NotNil(t, samples[10].RXBPS)
	assert.Equal(t, 80.0, *samples[10].RXBPS)

	gapJSON, err := json.Marshal(samples[1])
	require.NoError(t, err)
	assert.JSONEq(t, `{"timestamp":"2026-07-13T12:00:30Z","rx_bps":null,"tx_bps":null}`, string(gapJSON))
	zeroJSON, err := json.Marshal(samples[0])
	require.NoError(t, err)
	assert.JSONEq(t, `{"timestamp":"2026-07-13T12:00:00Z","rx_bps":0,"tx_bps":0}`, string(zeroJSON))
}

func TestFillTrafficHistoryGapsRangeBoundsAndCounts(t *testing.T) {
	observedAt := time.Date(2026, 7, 13, 12, 34, 47, 0, time.UTC)
	for _, rangeValue := range []string{"1m", "5m", "1h", "24h", "7d", "30d"} {
		t.Run(rangeValue, func(t *testing.T) {
			spec := trafficHistoryRanges[rangeValue]
			samples := fillTrafficHistoryGaps(spec, observedAt, nil)
			require.Len(t, samples, int(spec.window/spec.bucket)+1)
			assert.Equal(t, observedAt.Add(-spec.window).Truncate(spec.bucket), samples[0].Timestamp)
			assert.Equal(t, observedAt.Truncate(spec.bucket), samples[len(samples)-1].Timestamp)
			for index, sample := range samples {
				assert.Nil(t, sample.RXBPS)
				assert.Nil(t, sample.TXBPS)
				if index > 0 {
					assert.Equal(t, spec.bucket, sample.Timestamp.Sub(samples[index-1].Timestamp))
				}
			}
		})
	}
}

func TestResourcePercents(t *testing.T) {
	cpu, memory := resourcePercents(map[string]any{
		"cpu_percent":            json.Number("42.5"),
		"memory_total_bytes":     float64(1000),
		"memory_available_bytes": float64(250),
	})
	if assert.NotNil(t, cpu) {
		assert.Equal(t, 42.5, *cpu)
	}
	if assert.NotNil(t, memory) {
		assert.Equal(t, 75.0, *memory)
	}

	cpu, memory = resourcePercents(map[string]any{
		"cpu_percent":            101,
		"memory_total_bytes":     100,
		"memory_available_bytes": 101,
	})
	assert.Nil(t, cpu)
	assert.Nil(t, memory)
}

func TestAverageDelta(t *testing.T) {
	average, first, last := 30.0, 10.0, 35.0
	observedFrom := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	observedTo := observedFrom.Add(time.Minute)
	metric := averageDelta(&average, &first, &last, 3, &observedFrom, &observedTo)
	require.NotNil(t, metric)
	assert.Equal(t, 30.0, *metric.Average)
	assert.Equal(t, 25.0, *metric.Delta)
	assert.Equal(t, int64(3), metric.SampleCount)
	assert.Equal(t, observedFrom, *metric.ObservedFrom)
	assert.Equal(t, observedTo, *metric.ObservedTo)
	assert.Nil(t, averageDelta(nil, &first, &last, 0, nil, nil))
}

func TestNodeTrafficHistory(t *testing.T) {
	first := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	f := &fakeStore{trafficHistory: TrafficHistory{
		Range: "24h", BucketSeconds: 300, Samples: []TrafficHistorySample{
			{Timestamp: first, RXBPS: float64Ref(800), TXBPS: float64Ref(400)},
			{Timestamp: first.Add(5 * time.Minute), RXBPS: float64Ref(1600), TXBPS: float64Ref(600)},
		},
	}}
	w := request(t, handler(f), http.MethodGet, "/api/v1/nodes/"+testNodeID+"/traffic/history?range=24h", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "24h", f.trafficHistoryRange)
	assert.JSONEq(t, `{
		"range":"24h",
		"bucket_seconds":300,
		"samples":[
			{"timestamp":"2026-07-13T12:00:00Z","rx_bps":800,"tx_bps":400},
			{"timestamp":"2026-07-13T12:05:00Z","rx_bps":1600,"tx_bps":600}
		]
	}`, w.Body.String())
}

func TestNodeTrafficHistoryValidationAndAuth(t *testing.T) {
	h := handler(&fakeStore{})
	for _, path := range []string{
		"/api/v1/nodes/" + testNodeID + "/traffic/history",
		"/api/v1/nodes/" + testNodeID + "/traffic/history?range=1d",
		"/api/v1/nodes/" + testNodeID + "/traffic/history?range=1h&range=24h",
	} {
		w := request(t, h, http.MethodGet, path, "", testAdminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code, path+": "+w.Body.String())
	}
	w := request(t, h, http.MethodGet, "/api/v1/nodes/"+testNodeID+"/traffic/history?range=1h", "", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

func TestNodeTrafficHistoryReturnsNotFoundForUnknownNode(t *testing.T) {
	f := &fakeStore{trafficHistoryErr: ErrNotFound}
	w := request(t, handler(f), http.MethodGet, "/api/v1/nodes/"+testNodeID+"/traffic/history?range=7d", "", testAdminToken)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestRouteTrafficHistory(t *testing.T) {
	first := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	f := &fakeStore{routeTrafficHistory: TrafficHistory{
		Range: "5m", BucketSeconds: 30, Samples: []TrafficHistorySample{
			{Timestamp: first, RXBPS: float64Ref(320), TXBPS: float64Ref(160)},
			{Timestamp: first.Add(30 * time.Second), RXBPS: float64Ref(640), TXBPS: float64Ref(240)},
		},
	}}
	w := request(t, handler(f), http.MethodGet,
		"/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID+"/traffic/history?range=5m", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, testNodeID, f.routeHistoryNodeID)
	assert.Equal(t, testRouteID, f.routeHistoryRouteID)
	assert.Equal(t, "5m", f.routeHistoryRange)
	assert.JSONEq(t, `{
		"range":"5m",
		"bucket_seconds":30,
		"samples":[
			{"timestamp":"2026-07-13T12:00:00Z","rx_bps":320,"tx_bps":160},
			{"timestamp":"2026-07-13T12:00:30Z","rx_bps":640,"tx_bps":240}
		]
	}`, w.Body.String())
}

func TestRouteTrafficHistoryValidationAuthAndOwnership(t *testing.T) {
	h := handler(&fakeStore{})
	for _, path := range []string{
		"/api/v1/nodes/" + testNodeID + "/routes/" + testRouteID + "/traffic/history",
		"/api/v1/nodes/" + testNodeID + "/routes/" + testRouteID + "/traffic/history?range=1d",
		"/api/v1/nodes/" + testNodeID + "/routes/" + testRouteID + "/traffic/history?range=1h&range=24h",
	} {
		w := request(t, h, http.MethodGet, path, "", testAdminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code, path+": "+w.Body.String())
	}
	w := request(t, h, http.MethodGet,
		"/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID+"/traffic/history?range=1h", "", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())

	f := &fakeStore{routeHistoryErr: ErrNotFound}
	w = request(t, handler(f), http.MethodGet,
		"/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID+"/traffic/history?range=7d", "", testAdminToken)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}
