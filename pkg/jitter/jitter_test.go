package jitter

import (
	"testing"
	"time"
)

func TestCalculate(t *testing.T) {
	samples := []time.Duration{
		10 * time.Millisecond,
		12 * time.Millisecond,
		9 * time.Millisecond,
		15 * time.Millisecond,
		11 * time.Millisecond,
	}

	metric := Calculate(samples, 5)
	if metric.Samples != 5 {
		t.Fatalf("Expected 5 samples, got %d", metric.Samples)
	}
	if metric.JitterMs <= 0 {
		t.Errorf("Expected positive jitter, got %f", metric.JitterMs)
	}
	if metric.ScoreMOS < 1.0 || metric.ScoreMOS > 4.5 {
		t.Errorf("MOS score out of bounds: %f", metric.ScoreMOS)
	}
	t.Logf("Calculated Jitter: %.3f ms | MOS Score: %.2f", metric.JitterMs, metric.ScoreMOS)
}
