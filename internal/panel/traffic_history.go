package panel

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const trafficRateRetention = 31 * 24 * time.Hour

type TrafficHistorySample struct {
	Timestamp     time.Time `json:"timestamp"`
	RXBPS         *float64  `json:"rx_bps"`
	TXBPS         *float64  `json:"tx_bps"`
	CPUPercent    *float64  `json:"cpu_percent,omitempty"`
	MemoryPercent *float64  `json:"memory_percent,omitempty"`
}

type TrafficHistory struct {
	Range         string                 `json:"range"`
	BucketSeconds int64                  `json:"bucket_seconds"`
	Samples       []TrafficHistorySample `json:"samples"`
}

type trafficHistoryRange struct {
	window time.Duration
	bucket time.Duration
}

var trafficHistoryRanges = map[string]trafficHistoryRange{
	"1m":  {window: time.Minute, bucket: 15 * time.Second},
	"5m":  {window: 5 * time.Minute, bucket: 30 * time.Second},
	"1h":  {window: time.Hour, bucket: time.Minute},
	"24h": {window: 24 * time.Hour, bucket: 5 * time.Minute},
	"7d":  {window: 7 * 24 * time.Hour, bucket: 30 * time.Minute},
	"30d": {window: 30 * 24 * time.Hour, bucket: 2 * time.Hour},
}

func parseTrafficHistoryRange(value string) (trafficHistoryRange, bool) {
	spec, ok := trafficHistoryRanges[value]
	return spec, ok
}

func fillTrafficHistoryGaps(spec trafficHistoryRange, observedAt time.Time, samples []TrafficHistorySample) []TrafficHistorySample {
	firstBucket := observedAt.UTC().Add(-spec.window).Truncate(spec.bucket)
	lastBucket := observedAt.UTC().Truncate(spec.bucket)
	byBucket := make(map[time.Time]TrafficHistorySample, len(samples))
	for _, sample := range samples {
		bucket := sample.Timestamp.UTC().Truncate(spec.bucket)
		if bucket.Before(firstBucket) || bucket.After(lastBucket) {
			continue
		}
		sample.Timestamp = bucket
		byBucket[bucket] = sample
	}

	result := make([]TrafficHistorySample, 0, int(spec.window/spec.bucket)+1)
	for bucket := firstBucket; !bucket.After(lastBucket); bucket = bucket.Add(spec.bucket) {
		sample, ok := byBucket[bucket]
		if !ok {
			sample = TrafficHistorySample{Timestamp: bucket}
		}
		result = append(result, sample)
	}
	return result
}

func (a *API) nodeTrafficHistory(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	values, supplied := r.URL.Query()["range"]
	if !supplied || len(values) != 1 {
		writeError(w, http.StatusBadRequest, "validation_error", "range must be one of 1m, 5m, 1h, 24h, 7d, 30d")
		return
	}
	if _, ok := parseTrafficHistoryRange(values[0]); !ok {
		writeError(w, http.StatusBadRequest, "validation_error", "range must be one of 1m, 5m, 1h, 24h, 7d, 30d")
		return
	}
	v, err := a.store.GetTrafficHistory(r.Context(), nodeID, values[0])
	respondStore(w, v, err, http.StatusOK)
}

func (a *API) routeTrafficHistory(w http.ResponseWriter, r *http.Request, nodeID, routeID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	values, supplied := r.URL.Query()["range"]
	if !supplied || len(values) != 1 {
		writeError(w, http.StatusBadRequest, "validation_error", "range must be one of 1m, 5m, 1h, 24h, 7d, 30d")
		return
	}
	if _, ok := parseTrafficHistoryRange(values[0]); !ok {
		writeError(w, http.StatusBadRequest, "validation_error", "range must be one of 1m, 5m, 1h, 24h, 7d, 30d")
		return
	}
	v, err := a.store.GetRouteTrafficHistory(r.Context(), nodeID, routeID, values[0])
	respondStore(w, v, err, http.StatusOK)
}

func (s *PGStore) GetTrafficHistory(ctx context.Context, nodeID, rangeValue string) (TrafficHistory, error) {
	spec, ok := parseTrafficHistoryRange(rangeValue)
	if !ok {
		return TrafficHistory{}, errors.New("invalid traffic history range")
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1)`, nodeID).Scan(&exists); err != nil {
		return TrafficHistory{}, err
	}
	if !exists {
		return TrafficHistory{}, ErrNotFound
	}

	result := TrafficHistory{
		Range:         rangeValue,
		BucketSeconds: int64(spec.bucket / time.Second),
		Samples:       make([]TrafficHistorySample, 0),
	}
	observedAt := time.Now().UTC()
	rows, err := s.pool.Query(ctx, `
		SELECT date_bin($2::interval, sampled_at, TIMESTAMPTZ '1970-01-01 00:00:00+00') AS bucket_at,
		       avg(rx_bytes_per_second) * 8,
		       avg(tx_bytes_per_second) * 8,
		       avg(cpu_percent),avg(memory_percent)
		FROM node_traffic_rate_samples
		WHERE node_id=$1
		  AND sampled_at >= $3::timestamptz - $4::interval
		  AND sampled_at <= $3::timestamptz
		GROUP BY bucket_at
		ORDER BY bucket_at`, nodeID, postgresInterval(spec.bucket), observedAt, postgresInterval(spec.window))
	if err != nil {
		return TrafficHistory{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var sample TrafficHistorySample
		if err := rows.Scan(&sample.Timestamp, &sample.RXBPS, &sample.TXBPS, &sample.CPUPercent, &sample.MemoryPercent); err != nil {
			return TrafficHistory{}, err
		}
		result.Samples = append(result.Samples, sample)
	}
	if err = rows.Err(); err != nil {
		return TrafficHistory{}, err
	}
	result.Samples = fillTrafficHistoryGaps(spec, observedAt, result.Samples)
	return result, nil
}

func (s *PGStore) GetRouteTrafficHistory(ctx context.Context, nodeID, routeID, rangeValue string) (TrafficHistory, error) {
	spec, ok := parseTrafficHistoryRange(rangeValue)
	if !ok {
		return TrafficHistory{}, errors.New("invalid traffic history range")
	}
	result := TrafficHistory{
		Range:         rangeValue,
		BucketSeconds: int64(spec.bucket / time.Second),
		Samples:       make([]TrafficHistorySample, 0),
	}
	observedAt := time.Now().UTC()
	rows, err := s.pool.Query(ctx, `
		WITH requested_route AS (
			SELECT id
			FROM routes
			WHERE node_id=$1 AND id=$2
		), buckets AS (
			SELECT date_bin($3::interval,sampled_at,TIMESTAMPTZ '1970-01-01 00:00:00+00') AS bucket_at,
			       avg(rx_bytes_per_second)*8 AS rx_bps,
			       avg(tx_bytes_per_second)*8 AS tx_bps
			FROM route_traffic_rate_samples AS sample
			JOIN requested_route AS route ON route.id=sample.route_id
			WHERE sample.node_id=$1
			  AND sample.sampled_at >= $4::timestamptz-$5::interval
			  AND sample.sampled_at <= $4::timestamptz
			GROUP BY bucket_at
		)
		SELECT EXISTS(SELECT 1 FROM requested_route),bucket_at,rx_bps,tx_bps
		FROM (SELECT true) AS status
		LEFT JOIN buckets ON true
		ORDER BY bucket_at`, nodeID, routeID, postgresInterval(spec.bucket), observedAt, postgresInterval(spec.window))
	if err != nil {
		return TrafficHistory{}, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var exists bool
		var timestamp *time.Time
		var rxBPS, txBPS *float64
		if err = rows.Scan(&exists, &timestamp, &rxBPS, &txBPS); err != nil {
			return TrafficHistory{}, err
		}
		found = found || exists
		if timestamp != nil {
			result.Samples = append(result.Samples, TrafficHistorySample{
				Timestamp: timestamp.UTC(),
				RXBPS:     rxBPS,
				TXBPS:     txBPS,
			})
		}
	}
	if err = rows.Err(); err != nil {
		return TrafficHistory{}, err
	}
	if !found {
		return TrafficHistory{}, ErrNotFound
	}
	result.Samples = fillTrafficHistoryGaps(spec, observedAt, result.Samples)
	return result, nil
}

func (s *PGStore) getNodeMetricSummary(ctx context.Context, nodeID, rangeValue string) (*NodeMetricSummary, error) {
	spec, ok := parseTrafficHistoryRange(rangeValue)
	if !ok {
		return nil, errors.New("invalid metric summary range")
	}
	var summary NodeMetricSummary
	summary.Range = rangeValue
	var cpuAverage, cpuFirst, cpuLast *float64
	var cpuSampleCount int64
	var cpuObservedFrom, cpuObservedTo *time.Time
	var memoryAverage, memoryFirst, memoryLast *float64
	var memorySampleCount int64
	var memoryObservedFrom, memoryObservedTo *time.Time
	var rxAverage, rxFirst, rxLast *float64
	var rxSampleCount int64
	var rxObservedFrom, rxObservedTo *time.Time
	var txAverage, txFirst, txLast *float64
	var txSampleCount int64
	var txObservedFrom, txObservedTo *time.Time
	// First/last values use PK-ordered LIMIT 1 lookups instead of building and
	// sorting two full array_agg arrays per metric over the whole window.
	err := s.pool.QueryRow(ctx, `
		WITH bounds AS (SELECT clock_timestamp() - $2::interval AS since)
		SELECT count(*),min(sampled_at),max(sampled_at),
		       count(*) FILTER (WHERE cpu_percent IS NOT NULL),
		       min(sampled_at) FILTER (WHERE cpu_percent IS NOT NULL),
		       max(sampled_at) FILTER (WHERE cpu_percent IS NOT NULL),
		       avg(cpu_percent),
		       (SELECT sample.cpu_percent FROM node_traffic_rate_samples AS sample
		        WHERE sample.node_id=$1 AND sample.sampled_at >= (SELECT since FROM bounds) AND sample.cpu_percent IS NOT NULL
		        ORDER BY sample.sampled_at LIMIT 1),
		       (SELECT sample.cpu_percent FROM node_traffic_rate_samples AS sample
		        WHERE sample.node_id=$1 AND sample.sampled_at >= (SELECT since FROM bounds) AND sample.cpu_percent IS NOT NULL
		        ORDER BY sample.sampled_at DESC LIMIT 1),
		       count(*) FILTER (WHERE memory_percent IS NOT NULL),
		       min(sampled_at) FILTER (WHERE memory_percent IS NOT NULL),
		       max(sampled_at) FILTER (WHERE memory_percent IS NOT NULL),
		       avg(memory_percent),
		       (SELECT sample.memory_percent FROM node_traffic_rate_samples AS sample
		        WHERE sample.node_id=$1 AND sample.sampled_at >= (SELECT since FROM bounds) AND sample.memory_percent IS NOT NULL
		        ORDER BY sample.sampled_at LIMIT 1),
		       (SELECT sample.memory_percent FROM node_traffic_rate_samples AS sample
		        WHERE sample.node_id=$1 AND sample.sampled_at >= (SELECT since FROM bounds) AND sample.memory_percent IS NOT NULL
		        ORDER BY sample.sampled_at DESC LIMIT 1),
		       count(*) FILTER (WHERE rx_bytes_per_second IS NOT NULL),
		       min(sampled_at) FILTER (WHERE rx_bytes_per_second IS NOT NULL),
		       max(sampled_at) FILTER (WHERE rx_bytes_per_second IS NOT NULL),
		       avg(rx_bytes_per_second)*8,
		       (SELECT sample.rx_bytes_per_second*8 FROM node_traffic_rate_samples AS sample
		        WHERE sample.node_id=$1 AND sample.sampled_at >= (SELECT since FROM bounds) AND sample.rx_bytes_per_second IS NOT NULL
		        ORDER BY sample.sampled_at LIMIT 1),
		       (SELECT sample.rx_bytes_per_second*8 FROM node_traffic_rate_samples AS sample
		        WHERE sample.node_id=$1 AND sample.sampled_at >= (SELECT since FROM bounds) AND sample.rx_bytes_per_second IS NOT NULL
		        ORDER BY sample.sampled_at DESC LIMIT 1),
		       count(*) FILTER (WHERE tx_bytes_per_second IS NOT NULL),
		       min(sampled_at) FILTER (WHERE tx_bytes_per_second IS NOT NULL),
		       max(sampled_at) FILTER (WHERE tx_bytes_per_second IS NOT NULL),
		       avg(tx_bytes_per_second)*8,
		       (SELECT sample.tx_bytes_per_second*8 FROM node_traffic_rate_samples AS sample
		        WHERE sample.node_id=$1 AND sample.sampled_at >= (SELECT since FROM bounds) AND sample.tx_bytes_per_second IS NOT NULL
		        ORDER BY sample.sampled_at LIMIT 1),
		       (SELECT sample.tx_bytes_per_second*8 FROM node_traffic_rate_samples AS sample
		        WHERE sample.node_id=$1 AND sample.sampled_at >= (SELECT since FROM bounds) AND sample.tx_bytes_per_second IS NOT NULL
		        ORDER BY sample.sampled_at DESC LIMIT 1)
		FROM node_traffic_rate_samples
		WHERE node_id=$1 AND sampled_at >= (SELECT since FROM bounds)`,
		nodeID, postgresInterval(spec.window)).Scan(
		&summary.SampleCount, &summary.From, &summary.To,
		&cpuSampleCount, &cpuObservedFrom, &cpuObservedTo,
		&cpuAverage, &cpuFirst, &cpuLast,
		&memorySampleCount, &memoryObservedFrom, &memoryObservedTo,
		&memoryAverage, &memoryFirst, &memoryLast,
		&rxSampleCount, &rxObservedFrom, &rxObservedTo,
		&rxAverage, &rxFirst, &rxLast,
		&txSampleCount, &txObservedFrom, &txObservedTo,
		&txAverage, &txFirst, &txLast,
	)
	if err != nil {
		return nil, err
	}
	if summary.SampleCount == 0 {
		return nil, nil
	}
	summary.CPUPercent = averageDelta(cpuAverage, cpuFirst, cpuLast, cpuSampleCount, cpuObservedFrom, cpuObservedTo)
	summary.MemoryPercent = averageDelta(memoryAverage, memoryFirst, memoryLast, memorySampleCount, memoryObservedFrom, memoryObservedTo)
	summary.RXBPS = averageDelta(rxAverage, rxFirst, rxLast, rxSampleCount, rxObservedFrom, rxObservedTo)
	summary.TXBPS = averageDelta(txAverage, txFirst, txLast, txSampleCount, txObservedFrom, txObservedTo)
	return &summary, nil
}

func averageDelta(average, first, last *float64, sampleCount int64, observedFrom, observedTo *time.Time) *MetricAverageDelta {
	if average == nil {
		return nil
	}
	metric := &MetricAverageDelta{
		Average:      average,
		SampleCount:  sampleCount,
		ObservedFrom: observedFrom,
		ObservedTo:   observedTo,
	}
	if first != nil && last != nil {
		delta := *last - *first
		metric.Delta = &delta
	}
	return metric
}

func postgresInterval(value time.Duration) string {
	return strconv.FormatInt(int64(value/time.Second), 10) + " seconds"
}

func nonNegativeFiniteFloat(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	default:
		return 0, false
	}
	return number, number >= 0 && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func resourcePercents(metrics map[string]any) (cpuPercent, memoryPercent *float64) {
	if metrics == nil {
		return nil, nil
	}
	if value, ok := nonNegativeFiniteFloat(metrics["cpu_percent"]); ok && value <= 100 {
		cpuPercent = &value
	}
	total, totalOK := nonNegativeFiniteFloat(metrics["memory_total_bytes"])
	available, availableOK := nonNegativeFiniteFloat(metrics["memory_available_bytes"])
	if totalOK && availableOK && total > 0 && available <= total {
		value := (total - available) / total * 100
		memoryPercent = &value
	}
	return cpuPercent, memoryPercent
}

func recordHAProxyRateSamples(ctx context.Context, tx pgx.Tx, nodeID string, sampledAt time.Time, nodeRate trafficRate, routeRates map[string]trafficRate, metrics map[string]any) error {
	if !nodeRate.Valid && len(routeRates) == 0 {
		return nil
	}
	if nodeRate.Valid {
		cpuPercent, memoryPercent := resourcePercents(metrics)
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_traffic_rate_samples(
				node_id,sampled_at,rx_bytes_per_second,tx_bytes_per_second,cpu_percent,memory_percent
			) VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT(node_id,sampled_at) DO NOTHING`, nodeID, sampledAt,
			nodeRate.RXBytesPerSecond, nodeRate.TXBytesPerSecond, cpuPercent, memoryPercent); err != nil {
			return err
		}
	}

	routeIDs := make([]string, 0, len(routeRates))
	for routeID, rate := range routeRates {
		if rate.Valid {
			routeIDs = append(routeIDs, routeID)
		}
	}
	sort.Strings(routeIDs)
	if len(routeIDs) > 0 {
		rxRates := make([]float64, 0, len(routeIDs))
		txRates := make([]float64, 0, len(routeIDs))
		for _, routeID := range routeIDs {
			rxRates = append(rxRates, routeRates[routeID].RXBytesPerSecond)
			txRates = append(txRates, routeRates[routeID].TXBytesPerSecond)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO route_traffic_rate_samples(
				node_id,route_id,sampled_at,rx_bytes_per_second,tx_bytes_per_second
			)
			SELECT $1,route_rate.route_id::uuid,$2,route_rate.rx,route_rate.tx
			FROM unnest($3::text[],$4::float8[],$5::float8[])
			  AS route_rate(route_id,rx,tx)
			ON CONFLICT(node_id,route_id,sampled_at) DO NOTHING`,
			nodeID, sampledAt, routeIDs, rxRates, txRates); err != nil {
			return err
		}
	}
	// Retention is enforced by RunDataRetentionCleaner outside the heartbeat
	// transaction; no DELETE runs on the heartbeat critical path.
	return nil
}
