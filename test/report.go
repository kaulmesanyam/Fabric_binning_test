package test

import (
	"fabric_binning_test/core"
	"fmt"
	"os"
	"runtime"
	"time"
)

type ReportGenerator struct {
	results    []core.ScenarioResult
	outputFile *os.File
}

func NewReportGenerator(outputPath string) (*ReportGenerator, error) {
	file, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create report file: %v", err)
	}

	return &ReportGenerator{
		outputFile: file,
	}, nil
}

func (rg *ReportGenerator) AddScenarioResult(result core.ScenarioResult) {
	rg.results = append(rg.results, result)
}

func (rg *ReportGenerator) GenerateReport() error {
	defer rg.outputFile.Close()

	rg.writeHeader()

	// Write individual scenario results
	for _, result := range rg.results {
		rg.writeScenarioResults(result)
	}

	rg.writeOverallAnalysis()
	rg.writeRecommendations()

	return nil
}

func (rg *ReportGenerator) writeHeader() {
	rg.writeLine("===============================================")
	rg.writeLine("HYPERLEDGER FABRIC BINNING STRATEGY COMPARISON")
	rg.writeLine("===============================================")
	rg.writeLine("Generated at: %s\n", time.Now().Format(time.RFC1123))

	rg.writeLine("Test Configuration:")
	rg.writeLine("- System CPU Cores: %d", runtime.NumCPU())
	rg.writeLine("- Transactions per Test: 1000")
	rg.writeLine("- Maximum Bin Size: 25")
	rg.writeLine("- Block Size: 50")
	rg.writeLine("- Test Duration per Strategy: 1 minute\n")
	rg.writeLine("==============================================\n")
}

func (rg *ReportGenerator) writeScenarioResults(result core.ScenarioResult) {
	rg.writeLine("\n%s (%.0f%% Dependency)", getScenarioName(result.DependencyRatio), result.DependencyRatio*100)
	rg.writeLine("----------------------------------------")

	// Independent Strategy Results
	rg.writeLine("\nIndependent Binning Strategy:")
	rg.writeMetrics(result.IndependentMetrics)

	// Dependent Strategy Results
	rg.writeLine("\nDependent Binning Strategy:")
	rg.writeMetrics(result.DependentMetrics)

	// Comparison Analysis
	rg.writeLine("\nComparative Analysis:")
	rg.writeComparison(result)
	rg.writeLine("\n==============================================")
}

func (rg *ReportGenerator) writeMetrics(metrics core.DetailedMetrics) {
	if metrics.ThroughputTPS == 0 {
		rg.writeLine("TEST TIMEOUT - Incomplete Results")
		return
	}

	rg.writeLine("Performance Metrics:")
	rg.writeLine("- Throughput: %.2f TPS", metrics.ThroughputTPS)
	rg.writeLine("- Total Processing Time: %v", metrics.ProcessingTime)
	rg.writeLine("- Average Latency: %v", metrics.AverageLatency)

	rg.writeLine("\nTransaction Metrics:")
	rg.writeLine("- Total Transactions: %d", metrics.TotalTransactions)
	rg.writeLine("- Valid Transactions: %d (%.1f%%)",
		metrics.ValidTransactions,
		float64(metrics.ValidTransactions)/float64(metrics.TotalTransactions)*100)
	rg.writeLine("- Invalid Transactions: %d (%.1f%%)",
		metrics.InvalidTransactions,
		float64(metrics.InvalidTransactions)/float64(metrics.TotalTransactions)*100)
	rg.writeLine("- Re-simulated Transactions: %d (%.1f%%)",
		metrics.ReSimulatedTransactions,
		float64(metrics.ReSimulatedTransactions)/float64(metrics.TotalTransactions)*100)

	rg.writeLine("\nResource Usage:")
	rg.writeLine("- CPU Usage: %.1f%%", metrics.ResourceUsage.CPUUsage)
	rg.writeLine("- Memory Usage: %.1f MB", metrics.ResourceUsage.MemoryUsage)
	rg.writeLine("- Peak Goroutines: %d", metrics.ResourceUsage.NumGoroutines)
}

func (rg *ReportGenerator) writeComparison(result core.ScenarioResult) {
	if result.IndependentMetrics.ThroughputTPS == 0 || result.DependentMetrics.ThroughputTPS == 0 {
		rg.writeLine("Cannot perform comparison due to timeout in one or both strategies")
		return
	}

	// Calculate performance differences
	throughputDiff := calculatePercentageDifference(
		result.IndependentMetrics.ThroughputTPS,
		result.DependentMetrics.ThroughputTPS)

	validTxDiff := calculatePercentageDifference(
		float64(result.IndependentMetrics.ValidTransactions),
		float64(result.DependentMetrics.ValidTransactions))

	resimRateDiff := calculatePercentageDifference(
		float64(result.IndependentMetrics.ReSimulatedTransactions),
		float64(result.DependentMetrics.ReSimulatedTransactions))

	rg.writeLine("Performance Comparison:")
	rg.writeLine("- Throughput Difference: %.1f%%", throughputDiff)
	rg.writeLine("- Valid Transaction Rate Difference: %.1f%%", validTxDiff)
	rg.writeLine("- Re-simulation Rate Difference: %.1f%%", resimRateDiff)

	recommendedStrategy := determineRecommendedStrategy(result)
	rg.writeLine("\nRecommended Strategy: %s", recommendedStrategy)
	rg.writeStrategyReasoning(result)
}

func (rg *ReportGenerator) writeOverallAnalysis() {
	rg.writeLine("\nOVERALL ANALYSIS")
	rg.writeLine("===============")

	rg.writeLine("\n1. Performance Patterns:")
	for _, result := range rg.results {
		ratio := result.DependencyRatio * 100
		rg.writeLine("\nAt %.0f%% Dependency:", ratio)
		if result.IndependentMetrics.ThroughputTPS == 0 || result.DependentMetrics.ThroughputTPS == 0 {
			rg.writeLine("- Incomplete results due to timeout")
			continue
		}

		throughputDiff := calculatePercentageDifference(
			result.IndependentMetrics.ThroughputTPS,
			result.DependentMetrics.ThroughputTPS)

		rg.writeLine("- Independent Strategy: %.2f TPS", result.IndependentMetrics.ThroughputTPS)
		rg.writeLine("- Dependent Strategy: %.2f TPS", result.DependentMetrics.ThroughputTPS)
		rg.writeLine("- Performance Difference: %.1f%%", throughputDiff)
	}

	rg.writeLine("\n2. Resource Utilization Patterns:")
	rg.writeResourceAnalysis()
}

func (rg *ReportGenerator) writeResourceAnalysis() {
	for _, result := range rg.results {
		ratio := result.DependencyRatio * 100
		rg.writeLine("\nAt %.0f%% Dependency:", ratio)

		cpuDiff := calculatePercentageDifference(
			result.IndependentMetrics.ResourceUsage.CPUUsage,
			result.DependentMetrics.ResourceUsage.CPUUsage)

		memDiff := calculatePercentageDifference(
			result.IndependentMetrics.ResourceUsage.MemoryUsage,
			result.DependentMetrics.ResourceUsage.MemoryUsage)

		rg.writeLine("- CPU Usage: Independent=%.1f%%, Dependent=%.1f%% (Diff: %.1f%%)",
			result.IndependentMetrics.ResourceUsage.CPUUsage,
			result.DependentMetrics.ResourceUsage.CPUUsage,
			cpuDiff)

		rg.writeLine("- Memory Usage: Independent=%.1f MB, Dependent=%.1f MB (Diff: %.1f%%)",
			result.IndependentMetrics.ResourceUsage.MemoryUsage,
			result.DependentMetrics.ResourceUsage.MemoryUsage,
			memDiff)
	}
}

func (rg *ReportGenerator) writeRecommendations() {
	rg.writeLine("\nRECOMMENDATIONS")
	rg.writeLine("==============")

	rg.writeLine("\nBased on test results:")
	for _, result := range rg.results {
		ratio := result.DependencyRatio * 100
		rg.writeLine("\nFor %.0f%% dependency ratio:", ratio)
		if result.IndependentMetrics.ThroughputTPS == 0 || result.DependentMetrics.ThroughputTPS == 0 {
			rg.writeLine("- Insufficient data due to timeout")
			continue
		}

		recommended := determineRecommendedStrategy(result)
		rg.writeLine("- Recommended strategy: %s", recommended)
		rg.writeStrategyReasoning(result)
	}
}

func (rg *ReportGenerator) writeStrategyReasoning(result core.ScenarioResult) {
	if result.IndependentMetrics.ThroughputTPS == 0 || result.DependentMetrics.ThroughputTPS == 0 {
		return
	}

	indMetrics := result.IndependentMetrics
	depMetrics := result.DependentMetrics

	throughputDiff := calculatePercentageDifference(
		indMetrics.ThroughputTPS,
		depMetrics.ThroughputTPS)

	if throughputDiff > 0 {
		rg.writeLine("  * Independent strategy performs %.1f%% better in throughput", throughputDiff)
	} else {
		rg.writeLine("  * Dependent strategy performs %.1f%% better in throughput", -throughputDiff)
	}

	indInvalidRate := float64(indMetrics.InvalidTransactions) / float64(indMetrics.TotalTransactions) * 100
	depInvalidRate := float64(depMetrics.InvalidTransactions) / float64(depMetrics.TotalTransactions) * 100

	rg.writeLine("  * Invalid transaction rates: Independent=%.1f%%, Dependent=%.1f%%",
		indInvalidRate, depInvalidRate)
}

func (rg *ReportGenerator) writeLine(format string, args ...interface{}) {
	fmt.Fprintf(rg.outputFile, format+"\n", args...)
}

func getScenarioName(ratio float64) string {
	switch ratio {
	case 0.1:
		return "LOW CONTENTION SCENARIO"
	case 0.5:
		return "MIXED CASE SCENARIO"
	case 0.9:
		return "HIGH CONTENTION SCENARIO"
	default:
		return "UNKNOWN SCENARIO"
	}
}

func calculatePercentageDifference(val1, val2 float64) float64 {
	if val2 == 0 {
		return 0
	}
	return ((val1 - val2) / val2) * 100
}

func determineRecommendedStrategy(result core.ScenarioResult) string {
	if result.IndependentMetrics.ThroughputTPS > result.DependentMetrics.ThroughputTPS {
		return "Independent Binning"
	}
	return "Dependent Binning"
}
