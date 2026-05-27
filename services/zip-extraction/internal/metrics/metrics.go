// Package metrics constructs and registers the 8 Prometheus collectors per
// FR-13.2 + NFR-Z-060. Methods on *Metrics are concurrent-safe via the
// prometheus client library.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is the typed collector facade consumed via the extraction.Metrics port.
type Metrics struct {
	entriesTotal            *prometheus.CounterVec
	extractionDuration      *prometheus.HistogramVec
	extractionFailures      *prometheus.CounterVec
	bombRejections          *prometheus.CounterVec
	bytesExtracted          prometheus.Counter
	partialFailures         prometheus.Counter
	redeliverySkips         prometheus.Counter
	slipsheetFailures       prometheus.Counter
	classificationSuccesses *prometheus.CounterVec
	classificationFailures  *prometheus.CounterVec
}

// New constructs a *Metrics and registers all collectors on reg.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		entriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zip_entries_total",
			Help: "Number of zip entries processed, by terminal entry status.",
		}, []string{"status"}),
		extractionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "zip_extraction_duration_seconds",
			Help:    "End-to-end extraction duration, by terminal outcome.",
			Buckets: []float64{1, 5, 15, 30, 60, 120, 180, 220, 240},
		}, []string{"outcome"}),
		extractionFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zip_extraction_failures_total",
			Help: "Count of FAILED pipeline executions, by failure reason.",
		}, []string{"reason"}),
		bombRejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zip_bomb_rejections_total",
			Help: "Count of bomb-defence rejections, by FR-7 rule number.",
		}, []string{"rule"}),
		bytesExtracted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "extracted_bytes_total",
			Help: "Total decompressed bytes successfully uploaded to S3.",
		}),
		partialFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "partial_failures_total",
			Help: "Count of PARTIAL_FAILED pipeline executions.",
		}),
		redeliverySkips: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "redelivery_skips_total",
			Help: "Count of DynamoDB ConditionalCheckFailedException occurrences (idempotent re-delivery indicator).",
		}),
		slipsheetFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "slipsheet_write_failures_total",
			Help: "Count of slipsheet S3 write failures.",
		}),
		classificationSuccesses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "classification_calls_total",
			Help: "Count of successful classification calls, by returned category.",
		}, []string{"category"}),
		classificationFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "classification_failures_total",
			Help: "Count of classification call failures, by failure reason (download | http).",
		}, []string{"reason"}),
	}
	reg.MustRegister(
		m.entriesTotal,
		m.extractionDuration,
		m.extractionFailures,
		m.bombRejections,
		m.bytesExtracted,
		m.partialFailures,
		m.redeliverySkips,
		m.slipsheetFailures,
		m.classificationSuccesses,
		m.classificationFailures,
	)
	return m
}

// EntryProcessed implements extraction.Metrics.
func (m *Metrics) EntryProcessed(status string) {
	m.entriesTotal.WithLabelValues(status).Inc()
}

// ExtractionDuration implements extraction.Metrics.
func (m *Metrics) ExtractionDuration(d time.Duration, outcome string) {
	m.extractionDuration.WithLabelValues(outcome).Observe(d.Seconds())
}

// ExtractionFailure implements extraction.Metrics.
func (m *Metrics) ExtractionFailure(reason string) {
	m.extractionFailures.WithLabelValues(reason).Inc()
}

// BombRejection implements extraction.Metrics.
func (m *Metrics) BombRejection(rule int) {
	m.bombRejections.WithLabelValues(itoa(rule)).Inc()
}

// BytesExtracted implements extraction.Metrics.
func (m *Metrics) BytesExtracted(n int64) {
	m.bytesExtracted.Add(float64(n))
}

// PartialFailure implements extraction.Metrics.
func (m *Metrics) PartialFailure() { m.partialFailures.Inc() }

// RedeliverySkip implements extraction.Metrics.
func (m *Metrics) RedeliverySkip() { m.redeliverySkips.Inc() }

// SlipsheetWriteFailure implements extraction.Metrics.
func (m *Metrics) SlipsheetWriteFailure() { m.slipsheetFailures.Inc() }

// ClassificationSuccess implements extraction.Metrics.
func (m *Metrics) ClassificationSuccess(category string) {
	m.classificationSuccesses.WithLabelValues(category).Inc()
}

// ClassificationFailure implements extraction.Metrics.
func (m *Metrics) ClassificationFailure(reason string) {
	m.classificationFailures.WithLabelValues(reason).Inc()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
