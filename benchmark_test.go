package simslim

import (
	"reflect"
	"testing"
	"time"
)

func TestComputeStats(t *testing.T) {
	tests := []struct {
		name    string
		samples []int64
		want    MemoryStats
	}{
		{name: "empty", samples: nil, want: MemoryStats{}},
		{
			name:    "single sample",
			samples: []int64{1000},
			want:    MemoryStats{Samples: []int64{1000}, MinBytes: 1000, MeanBytes: 1000, MaxBytes: 1000},
		},
		{
			name:    "spread across several samples",
			samples: []int64{1000, 3000, 2000},
			want:    MemoryStats{Samples: []int64{1000, 3000, 2000}, MinBytes: 1000, MeanBytes: 2000, MaxBytes: 3000},
		},
		{
			name:    "mean rounds down via integer division",
			samples: []int64{1000, 1000, 1001},
			want:    MemoryStats{Samples: []int64{1000, 1000, 1001}, MinBytes: 1000, MeanBytes: 1000, MaxBytes: 1001},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeStats(tt.samples); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("computeStats(%v) = %+v, want %+v", tt.samples, got, tt.want)
			}
		})
	}
}

func TestSumBenchmark(t *testing.T) {
	tests := []struct {
		name      string
		results   []BenchmarkResult
		wantStock int64
		wantSlim  int64
	}{
		{name: "empty"},
		{
			name: "single device",
			results: []BenchmarkResult{
				{Stock: MemoryStats{Samples: []int64{4000}, MeanBytes: 4000}, Slim: MemoryStats{Samples: []int64{1000}, MeanBytes: 1000}},
			},
			wantStock: 4000,
			wantSlim:  1000,
		},
		{
			name: "sums across devices",
			results: []BenchmarkResult{
				{Stock: MemoryStats{Samples: []int64{4000}, MeanBytes: 4000}, Slim: MemoryStats{Samples: []int64{1000}, MeanBytes: 1000}},
				{Stock: MemoryStats{Samples: []int64{2000}, MeanBytes: 2000}, Slim: MemoryStats{Samples: []int64{500}, MeanBytes: 500}},
			},
			wantStock: 6000,
			wantSlim:  1500,
		},
		{
			name: "device with no completed runs excluded from totals",
			results: []BenchmarkResult{
				{Stock: MemoryStats{Samples: []int64{4000}, MeanBytes: 4000}, Slim: MemoryStats{Samples: []int64{1000}, MeanBytes: 1000}},
				{Error: "boot timed out"},
			},
			wantStock: 4000,
			wantSlim:  1000,
		},
		{
			name: "device that errored after completing some runs still counted",
			results: []BenchmarkResult{
				{Stock: MemoryStats{Samples: []int64{4000}, MeanBytes: 4000}, Slim: MemoryStats{Samples: []int64{1000}, MeanBytes: 1000}},
				{Stock: MemoryStats{Samples: []int64{2000, 2200}, MeanBytes: 2100}, Slim: MemoryStats{Samples: []int64{500, 520}, MeanBytes: 510}, Error: "boot timed out on run 3"},
			},
			wantStock: 6100,
			wantSlim:  1510,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStock, gotSlim := sumBenchmark(tt.results)
			if gotStock != tt.wantStock || gotSlim != tt.wantSlim {
				t.Errorf("sumBenchmark() = (%d, %d), want (%d, %d)", gotStock, gotSlim, tt.wantStock, tt.wantSlim)
			}
		})
	}
}

// samples builds a settleSample history at the given seconds-offset/bytes
// pairs, for TestSettled's synthetic curves.
func samples(pairs ...int64) []settleSample {
	if len(pairs)%2 != 0 {
		panic("samples wants (seconds, bytes) pairs")
	}
	out := make([]settleSample, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, settleSample{at: time.Duration(pairs[i]) * time.Second, bytes: pairs[i+1]})
	}
	return out
}

func TestSettled(t *testing.T) {
	tests := []struct {
		name    string
		history []settleSample
		want    bool
	}{
		{name: "empty history", history: nil, want: false},
		{
			name:    "not enough time elapsed yet, even if flat so far",
			history: samples(0, 1000, 30, 1000, 60, 1000, 90, 1000),
			want:    false,
		},
		{
			name: "still climbing across the trailing window",
			// last 120s (120->240) grows 1520->1635, 7.57%: over threshold.
			history: samples(0, 1000, 30, 1200, 60, 1350, 90, 1450, 120, 1520, 150, 1570, 180, 1605, 210, 1625, 240, 1635),
			want:    false,
		},
		{
			name: "genuinely flat across the trailing window",
			// same curve continued to where it actually flattens: last 120s
			// (240->360) grows 1635->1642, 0.43%: within threshold.
			history: samples(0, 1000, 30, 1200, 60, 1350, 90, 1450, 120, 1520, 150, 1570, 180, 1605, 210, 1625, 240, 1635, 270, 1638, 300, 1640, 330, 1641, 360, 1642),
			want:    true,
		},
		{
			name: "a burst inside the window isn't hidden by a flat tail",
			// the last two polls alone (120->135: 1611->1612) look flat, but
			// a burst at t=45 is still inside the trailing 120s window, so
			// this must not settle -- this is the exact bug the windowed
			// check replaces a naive last-two-polls comparison to avoid.
			history: samples(0, 1000, 15, 1005, 30, 1008, 45, 1600, 60, 1605, 75, 1608, 90, 1609, 105, 1610, 120, 1611, 135, 1612),
			want:    false,
		},
		{
			name: "a sudden drop at the end must not read as settled",
			// mirrors a real observed run: footprint climbs cleanly for 120s
			// (1500->1780->2180->2520, a normal daemon-startup curve) then
			// the next poll drops back to 1480 -- a daemon crash/restart, or
			// Measure's ps/top snapshots landing on either side of process
			// churn and briefly undercounting. Comparing only the window's
			// oldest point to its newest would see "growth" of
			// (1480-2180)/2180 = -32%, trivially under the threshold, and
			// wrongly call that settled. This is the actual bug that
			// produced artificially low stock numbers before the spread
			// check replaced a directional growth check.
			history: samples(0, 1500, 40, 1780, 80, 2180, 120, 2520, 140, 1480),
			want:    false,
		},
		{
			name:    "a mid-window dip that fully recovers by the end must not read as settled either",
			history: samples(0, 2000, 20, 2000, 40, 1000, 60, 2000, 80, 2000, 100, 2000, 120, 2000, 140, 2000),
			want:    false,
		},
		{
			name: "a real still-climbing iOS 27.0 curve, slow enough to look deceptively flat over a short window",
			// a fresh iPhone 17 Pro / iOS 27.0 boot measured: 1.83GB at
			// bootstatus, 2.92GB at 60s, ~3.3GB at 4min (240s) and still
			// climbing ~40MB every 30s there -- a creep that a shorter or
			// looser window could mistake for a plateau. In MB: the trailing
			// 120s (120->240) goes 3120->3300, 5.8% growth: comfortably over
			// threshold, so this must not settle.
			history: samples(0, 1830, 60, 2920, 120, 3120, 180, 3220, 240, 3300),
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := settled(tt.history); got != tt.want {
				t.Errorf("settled(%v) = %v, want %v", tt.history, got, tt.want)
			}
		})
	}
}

func TestFullSlimProfileDisablesEverything(t *testing.T) {
	desired := fullSlimProfile().Desired()
	managed := SlimmableSet()
	if len(desired) != len(managed) {
		t.Fatalf("fullSlimProfile().Desired() has %d labels, want %d (the full managed set)", len(desired), len(managed))
	}
	for l := range managed {
		if !desired[l] {
			t.Errorf("fullSlimProfile().Desired() missing label %q", l)
		}
	}
}
