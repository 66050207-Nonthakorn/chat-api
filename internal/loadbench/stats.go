package loadbench

import (
	"math"
	"sort"
	"time"
)

// Summary describes a latency distribution in milliseconds. Percentiles use
// nearest-rank, so p95 is an observed sample rather than an interpolation.
type Summary struct {
	Count int     `json:"count"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
	MaxMS float64 `json:"max_ms"`
}

// Summarize converts duration samples to nearest-rank percentiles.
func Summarize(samples []time.Duration) Summary {
	if len(samples) == 0 {
		return Summary{}
	}
	millis := make([]float64, len(samples))
	for index, sample := range samples {
		millis[index] = float64(sample) / float64(time.Millisecond)
	}
	sort.Float64s(millis)
	return Summary{
		Count: len(millis),
		P50MS: nearestRank(millis, .50),
		P95MS: nearestRank(millis, .95),
		P99MS: nearestRank(millis, .99),
		MaxMS: millis[len(millis)-1],
	}
}

func nearestRank(sorted []float64, percentile float64) float64 {
	index := int(math.Ceil(float64(len(sorted))*percentile)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
