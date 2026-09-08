package main

import (
	"context"
	"fmt"
	"os"
	"time"

	cli "github.com/urfave/cli/v3"

	"github.com/mobai-app/simslim"
)

func cmdBenchmark(ctx context.Context, cmd *cli.Command) error {
	jsonOutput := cmd.Bool("json")
	udids := cmd.Args().Slice()
	if len(udids) == 0 {
		return fmt.Errorf("benchmark expects one or more simulator UDIDs (see `simslim list`)")
	}
	runs := int(cmd.Int("runs"))
	if runs < 1 {
		return fmt.Errorf("benchmark --runs must be at least 1")
	}
	preserveBootState := cmd.Bool("preserve-boot-state")

	var report simslim.Reporter
	if !jsonOutput {
		report = func(msg string) { fmt.Fprintln(os.Stderr, msg) }
	}

	out := simslim.FleetBenchmark(ctx, udids, runs, preserveBootState, report)
	if jsonOutput {
		return writeJSON(out)
	}
	printBenchmark(out, runs)
	return nil
}

func printBenchmark(out simslim.BenchmarkOutput, runs int) {
	fmt.Printf("%-30s %-24s %-24s %8s  %s\n", "SIMULATOR", "STOCK", "SLIM", "RATIO", "SETTLE (stock/slim)")
	for _, d := range out.Devices {
		name := truncate(fmt.Sprintf("%s · %s", d.Name, shortUDID(d.UDID)), 30)
		if d.Error != "" && len(d.Stock.Samples) == 0 {
			fmt.Printf("%-30s error: %s\n", name, d.Error)
			continue
		}
		fmt.Printf("%-30s %-24s %-24s %8s  %s\n", name,
			formatStats(d.Stock, runs), formatStats(d.Slim, runs),
			benchmarkRatio(d.Stock.MeanBytes, d.Slim.MeanBytes),
			formatSettle(d.Stock.SettleSeconds, d.Slim.SettleSeconds))
		if d.Error != "" {
			fmt.Printf("  %d/%d runs completed; stopped early: %s\n", len(d.Stock.Samples), runs, d.Error)
		}
		if n := countUnsettled(d.Stock.Settled) + countUnsettled(d.Slim.Settled); n > 0 {
			fmt.Printf("  %d sample(s) hit the settle cap still climbing; treat this device's numbers with caution\n", n)
		}
	}
	fmt.Printf("\nFleet total (mean): %s stock -> %s slim (%s)\n",
		humanBytes(out.TotalStockBytes), humanBytes(out.TotalSlimBytes),
		benchmarkRatio(out.TotalStockBytes, out.TotalSlimBytes))
}

// countUnsettled counts the samples that hit settleMaxWait without ever
// genuinely flattening, so their reading is just a last poll, not a
// confirmed plateau.
func countUnsettled(settled []bool) int {
	n := 0
	for _, ok := range settled {
		if !ok {
			n++
		}
	}
	return n
}

// formatSettle renders the average time each leg took to stop growing
// before it was sampled, so a slow-to-settle device is visible rather than
// hidden behind a single early reading.
func formatSettle(stockSeconds, slimSeconds []float64) string {
	return fmt.Sprintf("%s / %s", avgDuration(stockSeconds), avgDuration(slimSeconds))
}

func avgDuration(seconds []float64) string {
	if len(seconds) == 0 {
		return "—"
	}
	var sum float64
	for _, s := range seconds {
		sum += s
	}
	avg := time.Duration(sum / float64(len(seconds)) * float64(time.Second))
	return avg.Round(time.Second).String()
}

// formatStats renders a device's mean footprint, appending the min-max range
// once there is more than one sample to show a spread for.
func formatStats(stats simslim.MemoryStats, runs int) string {
	mean := humanBytes(stats.MeanBytes)
	if runs <= 1 || len(stats.Samples) <= 1 {
		return mean
	}
	return fmt.Sprintf("%s (%s-%s)", mean, humanBytes(stats.MinBytes), humanBytes(stats.MaxBytes))
}

// benchmarkRatio formats how many times smaller the slim footprint is, or
// "—" when there's nothing to divide by (a device that errored before it
// could be measured, for instance).
func benchmarkRatio(stockBytes, slimBytes int64) string {
	if slimBytes <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2fx", float64(stockBytes)/float64(slimBytes))
}
