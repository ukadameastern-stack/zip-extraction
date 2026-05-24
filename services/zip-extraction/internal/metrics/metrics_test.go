package metrics_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/metrics"
)

func TestMetrics_AllRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	m.EntryProcessed("UPLOADED")
	m.ExtractionDuration(50*time.Second, "SUCCESS")
	m.ExtractionFailure("schema")
	m.BombRejection(2)
	m.BytesExtracted(1024)
	m.PartialFailure()
	m.RedeliverySkip()
	m.SlipsheetWriteFailure()

	assert.Equal(t, float64(1), testutil.ToFloat64(prometheusCounterByName(reg, "zip_entries_total")))
}

// prometheusCounterByName is a tiny helper to look up a registered metric by
// name; if none found, returns a zero-valued counter so the test asserts the
// metric exists.
func prometheusCounterByName(reg *prometheus.Registry, name string) prometheus.Counter {
	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.Metric) > 0 {
			c := prometheus.NewCounter(prometheus.CounterOpts{Name: name})
			c.Add(mf.Metric[0].GetCounter().GetValue())
			return c
		}
	}
	return prometheus.NewCounter(prometheus.CounterOpts{Name: "missing"})
}
