package jitter

import (
	"math"
	"time"
)

// Metric summarizes packet jitter and loss characteristics per RFC 3550.
type Metric struct {
	Samples     int     `json:"samples"`
	MeanDelayMs float64 `json:"mean_delay_ms"`
	MinDelayMs  float64 `json:"min_delay_ms"`
	MaxDelayMs  float64 `json:"max_delay_ms"`
	JitterMs    float64 `json:"jitter_ms"`
	LossPercent float64 `json:"loss_percent"`
	ScoreMOS    float64 `json:"score_mos"` // Estimated Mean Opinion Score (1.0 - 4.5)
}

// CalculateRFC3550 computes statistical interarrival jitter over a series of latency samples.
func Calculate(delays []time.Duration, totalSent int) Metric {
	if len(delays) == 0 {
		return Metric{Samples: 0, LossPercent: 100.0, ScoreMOS: 1.0}
	}

	minVal := float64(delays[0].Microseconds()) / 1000.0
	maxVal := minVal
	var sum float64
	var jitter float64

	var prevDelay float64

	for i, d := range delays {
		ms := float64(d.Microseconds()) / 1000.0
		sum += ms
		if ms < minVal {
			minVal = ms
		}
		if ms > maxVal {
			maxVal = ms
		}

		if i > 0 {
			diff := math.Abs(ms - prevDelay)
			jitter += (diff - jitter) / 16.0 // RFC 3550 smoothing filter
		}
		prevDelay = ms
	}

	mean := sum / float64(len(delays))
	loss := 0.0
	if totalSent > 0 {
		loss = float64(totalSent-len(delays)) / float64(totalSent) * 100.0
	}

	// Simplified E-Model MOS estimation
	rFactor := 94.2 - (mean * 0.024) - (loss * 2.5) - (jitter * 0.1)
	if rFactor < 0 {
		rFactor = 0
	}
	mos := 1.0
	if rFactor > 0 {
		mos = 1.0 + (0.035 * rFactor) + (rFactor * (100 - rFactor) * (rFactor - 60) * 0.000007)
		if mos > 4.5 {
			mos = 4.5
		}
		if mos < 1.0 {
			mos = 1.0
		}
	}

	return Metric{
		Samples:     len(delays),
		MeanDelayMs: mean,
		MinDelayMs:  minVal,
		MaxDelayMs:  maxVal,
		JitterMs:    jitter,
		LossPercent: loss,
		ScoreMOS:    math.Round(mos*100) / 100,
	}
}
