package simslim

import (
	"context"
	"fmt"
	"time"
)

// MemoryStats summarizes repeated byte-footprint samples from one
// device/state (stock or slim) across a benchmark's runs.
type MemoryStats struct {
	Samples       []int64   `json:"samples"`                 // bytes, one per run, in order
	SettleSeconds []float64 `json:"settleSeconds,omitempty"` // how long each sample took to stop growing, parallel to Samples
	MinBytes      int64     `json:"minBytes"`
	MeanBytes     int64     `json:"meanBytes"`
	MaxBytes      int64     `json:"maxBytes"`
}

// computeStats summarizes a set of byte samples. An empty input returns the
// zero value.
func computeStats(samples []int64) MemoryStats {
	if len(samples) == 0 {
		return MemoryStats{}
	}
	stats := MemoryStats{Samples: samples, MinBytes: samples[0], MaxBytes: samples[0]}
	var sum int64
	for _, b := range samples {
		sum += b
		if b < stats.MinBytes {
			stats.MinBytes = b
		}
		if b > stats.MaxBytes {
			stats.MaxBytes = b
		}
	}
	stats.MeanBytes = sum / int64(len(samples))
	return stats
}

// BenchmarkResult is one device's stock-vs-slim memory comparison, over one
// or more runs.
type BenchmarkResult struct {
	Device
	Stock MemoryStats `json:"stock"`
	Slim  MemoryStats `json:"slim"`
	Error string      `json:"error,omitempty"`
}

// BenchmarkOutput is the fleet-wide result of FleetBenchmark: every device's
// comparison plus the summed mean totals across devices that completed at
// least one run.
type BenchmarkOutput struct {
	Devices         []BenchmarkResult `json:"devices"`
	TotalStockBytes int64             `json:"totalStockBytes"`
	TotalSlimBytes  int64             `json:"totalSlimBytes"`
}

// fullSlimProfile is the default `simslim on` applies with no --except/--keep:
// every managed daemon disabled.
func fullSlimProfile() Profile {
	return Profile{ExceptCategories: map[string]bool{}, Keep: map[string]bool{}}
}

// FleetBenchmark measures each device stock, slims it, measures again,
// repeating runs times, then restores it to stock (and, if
// preserveBootState, to its original boot state) before moving to the next
// device. A device that fails partway through records that failure in its
// own Error alongside whatever samples it completed, and does not abort the
// rest of the fleet. runs must be >= 1.
func FleetBenchmark(ctx context.Context, udids []string, runs int, preserveBootState bool, report Reporter) BenchmarkOutput {
	if runs < 1 {
		runs = 1
	}
	results := make([]BenchmarkResult, len(udids))
	for i, udid := range udids {
		results[i] = benchmarkDevice(ctx, udid, runs, preserveBootState, report)
	}
	stock, slim := sumBenchmark(results)
	return BenchmarkOutput{Devices: results, TotalStockBytes: stock, TotalSlimBytes: slim}
}

// sumBenchmark totals mean stock and slim bytes across devices that
// completed at least one run, including a device that later recorded an
// error (e.g. failed partway through a later run, or while restoring) as
// long as it left behind at least one completed stock/slim sample pair.
// Devices that errored before completing a single run contribute nothing.
func sumBenchmark(results []BenchmarkResult) (stockBytes, slimBytes int64) {
	for _, r := range results {
		if len(r.Stock.Samples) == 0 {
			continue
		}
		stockBytes += r.Stock.MeanBytes
		slimBytes += r.Slim.MeanBytes
	}
	return stockBytes, slimBytes
}

func benchmarkDevice(ctx context.Context, udid string, runs int, preserveBootState bool, report Reporter) BenchmarkResult {
	d, err := FindDevice(ctx, udid, "")
	if err != nil {
		return BenchmarkResult{Device: Device{UDID: udid}, Error: err.Error()}
	}
	originallyShutdown := preserveBootState && d.State == "Shutdown"

	var stockSamples, slimSamples []int64
	var stockSettle, slimSettle []float64
	var runErr error
	for i := 0; i < runs && runErr == nil; i++ {
		report.report(fmt.Sprintf("%s: run %d/%d: establishing stock baseline...", udid, i+1, runs))
		if _, err := timedDisableSlim(ctx, d, report); err != nil {
			runErr = err
			break
		}
		stock, err := settleMeasure(ctx, udid, "stock", report)
		if err != nil {
			runErr = err
			break
		}

		report.report(fmt.Sprintf("%s: run %d/%d: slimming...", udid, i+1, runs))
		if _, err := timedEnableSlim(ctx, d, report); err != nil {
			runErr = err
			break
		}
		slim, err := settleMeasure(ctx, udid, "slim", report)
		if err != nil {
			runErr = err
			break
		}

		stockSamples = append(stockSamples, stock.Bytes)
		stockSettle = append(stockSettle, stock.SettleSeconds)
		slimSamples = append(slimSamples, slim.Bytes)
		slimSettle = append(slimSettle, slim.SettleSeconds)
	}

	stockStats := computeStats(stockSamples)
	stockStats.SettleSeconds = stockSettle
	slimStats := computeStats(slimSamples)
	slimStats.SettleSeconds = slimSettle

	res := BenchmarkResult{Device: d, Stock: stockStats, Slim: slimStats}

	report.report(udid + ": restoring...")
	if err := restoreBenchmarkDevice(ctx, d, originallyShutdown, report); err != nil && runErr == nil {
		runErr = err
	}
	if runErr != nil {
		res.Error = runErr.Error()
	}
	return res
}

// Settle-detection tuning for benchmark measurements. simctl's `bootstatus -b`
// (and, for the slim leg, the disable/reboot cycle finishing) only means the
// simulator is up, not that its daemons are done starting: a stock boot keeps
// starting services, and its footprint keeps climbing, for minutes after that
// point, while a slim boot has far less left to start and settles much
// faster. Measuring right away understates stock far more than it
// understates slim, which understates the whole comparison. settleMeasure
// polls until the footprint stops climbing instead of trusting a single
// early sample.
//
// Growth isn't smooth: daemons start in bursts with gaps between them, so
// comparing only the last two polls can mistake a gap between bursts for the
// real plateau and stop far too early. settled() instead requires the whole
// trailing settleWindow to show under settleThreshold growth, not just the
// latest step, so a burst anywhere in that window keeps polling going.
const (
	settlePollInterval = 15 * time.Second
	settleWindow       = 60 * time.Second // require this much trailing time to look flat
	settleThreshold    = 0.02             // treat under 2% growth across settleWindow as settled
	settleMaxWait      = 6 * time.Minute
)

// settleMeasurement is a stabilized measurement: the settled footprint plus
// how long it took to stop growing.
type settleMeasurement struct {
	Measurement
	SettleSeconds float64
}

// settleSample is one poll's footprint, timestamped relative to when
// settleMeasure started.
type settleSample struct {
	at    time.Duration
	bytes int64
}

// settled reports whether every sample within the trailing settleWindow
// (including the latest) stays within settleThreshold of each other: the
// window's (max-min)/min spread, not just growth from its oldest point to
// its newest. A plain start-vs-end comparison would call a dip settled just
// because "growth" from an earlier high point is negative -- and a real dip
// does happen mid-boot (a daemon crashing and restarting, or Measure's `ps`
// and `top` snapshots landing on either side of a burst of process churn and
// briefly undercounting the tree), so a drop must keep polling going exactly
// like a climb does. history must be ordered oldest-first and include the
// latest sample. It refuses to declare settled before settleWindow has
// actually elapsed, so a device can't get lucky on a couple of
// coincidentally-close early polls.
func settled(history []settleSample) bool {
	if len(history) == 0 {
		return false
	}
	latest := history[len(history)-1]
	if latest.at < settleWindow {
		return false
	}
	minBytes, maxBytes := latest.bytes, latest.bytes
	for i := len(history) - 1; i >= 0 && latest.at-history[i].at <= settleWindow; i-- {
		if history[i].bytes < minBytes {
			minBytes = history[i].bytes
		}
		if history[i].bytes > maxBytes {
			maxBytes = history[i].bytes
		}
	}
	if minBytes <= 0 {
		return false
	}
	spread := float64(maxBytes-minBytes) / float64(minBytes)
	return spread <= settleThreshold
}

// settleMeasure polls Measure on the device until its footprint has been
// flat (see settled) over the trailing settleWindow, or settleMaxWait
// elapses, and reports progress along the way since settling a stock boot
// can take minutes. label identifies the leg being settled ("stock" or
// "slim") in progress messages.
func settleMeasure(ctx context.Context, udid, label string, report Reporter) (settleMeasurement, error) {
	start := time.Now()
	m, err := Measure(ctx, udid)
	if err != nil {
		return settleMeasurement{}, err
	}
	history := []settleSample{{at: 0, bytes: m.Bytes}}
	for time.Since(start) < settleMaxWait {
		select {
		case <-ctx.Done():
			return settleMeasurement{Measurement: m, SettleSeconds: time.Since(start).Seconds()}, ctx.Err()
		case <-time.After(settlePollInterval):
		}
		next, err := Measure(ctx, udid)
		if err != nil {
			return settleMeasurement{Measurement: m, SettleSeconds: time.Since(start).Seconds()}, err
		}
		m = next
		elapsed := time.Since(start)
		history = append(history, settleSample{at: elapsed, bytes: m.Bytes})
		report.report(fmt.Sprintf("%s: %s settling... %.2f GB at %s", udid, label, float64(m.Bytes)/(1<<30), elapsed.Round(time.Second)))
		if settled(history) {
			break
		}
	}
	return settleMeasurement{Measurement: m, SettleSeconds: time.Since(start).Seconds()}, nil
}

// restoreBenchmarkDevice returns a benchmarked device to stock and, if
// requested, back to the shutdown state it started in.
func restoreBenchmarkDevice(ctx context.Context, d Device, returnToShutdown bool, report Reporter) error {
	if _, err := timedDisableSlim(ctx, d, report); err != nil {
		return err
	}
	if !returnToShutdown {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, ShutdownTimeout)
	defer cancel()
	if err := Shutdown(shutdownCtx, d.Set, d.UDID); err != nil {
		return err
	}
	return WaitShutdown(shutdownCtx, d.Set, d.UDID, ShutdownTimeout)
}

func timedDisableSlim(ctx context.Context, d Device, report Reporter) (bool, error) {
	tctx, cancel := context.WithTimeout(ctx, BootTimeout)
	defer cancel()
	return DisableSlim(tctx, d.Set, d.UDID, report)
}

func timedEnableSlim(ctx context.Context, d Device, report Reporter) (bool, error) {
	tctx, cancel := context.WithTimeout(ctx, BootTimeout)
	defer cancel()
	return EnableSlim(tctx, d.Set, d.UDID, fullSlimProfile(), report)
}
