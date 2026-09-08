package eval

import (
	"math"
	"testing"
)

func TestStatsEmpty(t *testing.T) {
	mean, variance, stdDev, half := stats(nil)
	if mean != 0 || variance != 0 || stdDev != 0 || half != 0 {
		t.Fatalf("empty stats should be zero, got %v %v %v %v", mean, variance, stdDev, half)
	}
}

func TestStatsSingle(t *testing.T) {
	mean, variance, stdDev, half := stats([]float64{0.5})
	if mean != 0.5 || variance != 0 || stdDev != 0 || half != 0 {
		t.Fatalf("single-element stats wrong, got %v %v %v %v", mean, variance, stdDev, half)
	}
}

func TestStatsSample(t *testing.T) {
	// 样本 [0, 1, 0, 1]：均值 0.5，样本方差 1/3，CI 半宽被裁剪到 0.5（避免越界）。
	mean, variance, stdDev, half := stats([]float64{0, 1, 0, 1})
	if math.Abs(mean-0.5) > 1e-9 {
		t.Fatalf("mean should be 0.5, got %v", mean)
	}
	if math.Abs(variance-1.0/3.0) > 1e-9 {
		t.Fatalf("variance should be 1/3, got %v", variance)
	}
	if math.Abs(stdDev-math.Sqrt(1.0/3.0)) > 1e-9 {
		t.Fatalf("stdDev wrong, got %v", stdDev)
	}
	if math.Abs(half-0.5) > 1e-9 {
		t.Fatalf("CI half should be clipped to 0.5, got %v", half)
	}
}

func TestStatsAllSame(t *testing.T) {
	// 全命中：均值 1、方差 0、CI 半宽 0（确定性）。
	mean, variance, _, half := stats([]float64{1, 1, 1})
	if mean != 1 || variance != 0 || half != 0 {
		t.Fatalf("all-same stats wrong, got %v %v %v", mean, variance, half)
	}
}

func TestCosineSimilarity(t *testing.T) {
	if got := cosineSimilarity([]float32{1, 0}, []float32{1, 0}); math.Abs(got-1) > 1e-9 {
		t.Fatalf("identical vectors should be 1, got %v", got)
	}
	if got := cosineSimilarity([]float32{1, 0}, []float32{0, 1}); math.Abs(got) > 1e-9 {
		t.Fatalf("orthogonal vectors should be 0, got %v", got)
	}
	if got := cosineSimilarity([]float32{1, 0}, []float32{1}); got != 0 {
		t.Fatalf("mismatched length should be 0, got %v", got)
	}
}
