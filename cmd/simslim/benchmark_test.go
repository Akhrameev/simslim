package main

import (
	"testing"

	"github.com/mobai-app/simslim"
)

func TestBenchmarkRatio(t *testing.T) {
	tests := []struct {
		name       string
		stockBytes int64
		slimBytes  int64
		want       string
	}{
		{name: "typical reduction", stockBytes: 4000, slimBytes: 1000, want: "4.00x"},
		{name: "no reduction", stockBytes: 1000, slimBytes: 1000, want: "1.00x"},
		{name: "zero slim bytes has nothing to divide by", stockBytes: 1000, slimBytes: 0, want: "—"},
		{name: "negative slim bytes (defensive) also blank", stockBytes: 1000, slimBytes: -1, want: "—"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := benchmarkRatio(tt.stockBytes, tt.slimBytes); got != tt.want {
				t.Errorf("benchmarkRatio(%d, %d) = %q, want %q", tt.stockBytes, tt.slimBytes, got, tt.want)
			}
		})
	}
}

func TestFormatStats(t *testing.T) {
	tests := []struct {
		name  string
		stats simslim.MemoryStats
		runs  int
		want  string
	}{
		{
			name:  "single run omits range",
			stats: simslim.MemoryStats{Samples: []int64{1 << 20}, MeanBytes: 1 << 20, MinBytes: 1 << 20, MaxBytes: 1 << 20},
			runs:  1,
			want:  "1 MB",
		},
		{
			name:  "multiple runs show min-max range",
			stats: simslim.MemoryStats{Samples: []int64{1 << 20, 2 << 20, 3 << 20}, MeanBytes: 2 << 20, MinBytes: 1 << 20, MaxBytes: 3 << 20},
			runs:  3,
			want:  "2 MB (1 MB-3 MB)",
		},
		{
			name:  "runs requested but only one sample completed",
			stats: simslim.MemoryStats{Samples: []int64{1 << 20}, MeanBytes: 1 << 20, MinBytes: 1 << 20, MaxBytes: 1 << 20},
			runs:  3,
			want:  "1 MB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatStats(tt.stats, tt.runs); got != tt.want {
				t.Errorf("formatStats(%+v, %d) = %q, want %q", tt.stats, tt.runs, got, tt.want)
			}
		})
	}
}

func TestAvgDuration(t *testing.T) {
	tests := []struct {
		name    string
		seconds []float64
		want    string
	}{
		{name: "no samples", seconds: nil, want: "—"},
		{name: "single sample", seconds: []float64{90}, want: "1m30s"},
		{name: "averages and rounds to the nearest second", seconds: []float64{10, 20, 21}, want: "17s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := avgDuration(tt.seconds); got != tt.want {
				t.Errorf("avgDuration(%v) = %q, want %q", tt.seconds, got, tt.want)
			}
		})
	}
}

func TestFormatSettle(t *testing.T) {
	got := formatSettle([]float64{120, 140}, []float64{20, 30})
	want := "2m10s / 25s"
	if got != want {
		t.Errorf("formatSettle() = %q, want %q", got, want)
	}
}

func TestCountUnsettled(t *testing.T) {
	tests := []struct {
		name    string
		settled []bool
		want    int
	}{
		{name: "no samples", settled: nil, want: 0},
		{name: "all settled", settled: []bool{true, true, true}, want: 0},
		{name: "one hit the cap", settled: []bool{true, false, true}, want: 1},
		{name: "all hit the cap", settled: []bool{false, false}, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countUnsettled(tt.settled); got != tt.want {
				t.Errorf("countUnsettled(%v) = %d, want %d", tt.settled, got, tt.want)
			}
		})
	}
}

func TestCmdBenchmarkRequiresUDIDs(t *testing.T) {
	if err := runApp(t, "benchmark"); err == nil {
		t.Fatal("benchmark with no UDIDs: want error, got nil")
	}
}

func TestCmdBenchmarkRejectsNonPositiveRuns(t *testing.T) {
	if err := runApp(t, "benchmark", "--runs", "0", "some-udid"); err == nil {
		t.Fatal("benchmark --runs 0: want error, got nil")
	}
}
