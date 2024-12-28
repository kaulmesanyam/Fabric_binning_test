package test

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"fabric_binning_test/binning"
	"fabric_binning_test/committer"
	"fabric_binning_test/core"
	"fabric_binning_test/ordering"
	"fabric_binning_test/validation"
)

type TestConfig struct {
	Strategy          string
	TotalTransactions int
	DependencyRatio   float64
	MaxBinSize        int
	BlockSize         int
	TestDuration      time.Duration
}

func RunTests() {
	fmt.Printf("Starting Fabric Binning Strategy Comparison Tests\n")
	fmt.Printf("Running on %d CPU cores\n", runtime.NumCPU())

	// Initialize report generator
	reportGen, err := NewReportGenerator("binning_comparison_report.txt")
	if err != nil {
		fmt.Printf("Error creating report: %v\n", err)
		return
	}

	// Test configurations
	dependencyRatios := []float64{0.1, 0.5, 0.9}
	for _, ratio := range dependencyRatios {
		fmt.Printf("\n=== Testing with Dependency Ratio: %.1f ===\n", ratio)

		scenarioResult := core.ScenarioResult{
			DependencyRatio: ratio,
		}

		// Test independent strategy
		indConfig := TestConfig{
			Strategy:          "independent",
			TotalTransactions: 1000,
			DependencyRatio:   ratio,
			MaxBinSize:        25,               // Reduced size
			BlockSize:         50,               // Reduced size
			TestDuration:      time.Second * 30, // Reduced timeout
		}
		scenarioResult.IndependentMetrics = runTest(indConfig)

		// Force GC and pause between tests
		runtime.GC()
		time.Sleep(time.Second * 2)

		// Test dependent strategy
		depConfig := indConfig
		depConfig.Strategy = "dependent"
		scenarioResult.DependentMetrics = runTest(depConfig)

		reportGen.AddScenarioResult(scenarioResult)

		// Force GC and pause between scenarios
		runtime.GC()
		time.Sleep(time.Second * 2)
	}

	// Generate final report
	if err := reportGen.GenerateReport(); err != nil {
		fmt.Printf("Error generating report: %v\n", err)
		return
	}

	fmt.Printf("\nTests completed. Report generated: binning_comparison_report.txt\n")
}

func runTest(config TestConfig) core.DetailedMetrics {
	metrics := core.DetailedMetrics{
		Strategy:        config.Strategy,
		DependencyRatio: config.DependencyRatio,
	}

	startTime := time.Now()
	fmt.Printf("\nStarting test with %s strategy (Dependency Ratio: %.2f)\n",
		config.Strategy, config.DependencyRatio)

	// Create done channel for cleanup
	done := make(chan struct{})
	defer close(done)

	// Initialize components
	worldState := &core.WorldState{
		State: make(map[string]string),
	}

	binManager := binning.NewBinManager(config.Strategy, config.MaxBinSize, &metrics)
	orderingService := ordering.NewOrderingService(config.Strategy, &metrics)
	committerService := committer.NewCommitter(config.Strategy, &metrics)
	reSimManager := validation.NewReSimulationManager(worldState, &metrics)

	// Connect channels
	orderingService.BinChannel = binManager.BinChannel
	committerService.BlockChannel = orderingService.BlockChannel
	reSimManager.BinChannel = binManager.BinChannel
	committerService.ReSimChannel = reSimManager.ReSimulationQueue

	// Start services
	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		binManager.Start(done)
	}()

	go func() {
		defer wg.Done()
		orderingService.Start(done)
	}()

	go func() {
		defer wg.Done()
		committerService.Start(done)
	}()

	go func() {
		defer wg.Done()
		reSimManager.Start(done)
	}()

	// Generate and submit transactions with progress tracking
	transactions := generateTestTransactions(config)
	fmt.Printf("Generated %d test transactions\n", len(transactions))

	txSubmitted := 0
	submitStart := time.Now()

	for i, tx := range transactions {
		select {
		case binManager.TransactionPool <- tx:
			txSubmitted++
			if (i+1)%100 == 0 {
				fmt.Printf("Submitted %d transactions...\n", i+1)
			}
		case <-time.After(time.Millisecond * 100):
			fmt.Printf("Transaction submission timeout after %d transactions\n", txSubmitted)
			goto WaitForCompletion
		}
	}

	fmt.Printf("Submitted all transactions in %v\n", time.Since(submitStart))

WaitForCompletion:
	// Close transaction pool
	close(binManager.TransactionPool)

	// Wait with timeout
	completed := make(chan bool)
	go func() {
		wg.Wait()
		completed <- true
	}()

	var timedOut bool
	select {
	case <-completed:
		fmt.Println("Test completed normally")
	case <-time.After(config.TestDuration):
		fmt.Printf("Test timeout after %v\n", config.TestDuration)
		timedOut = true
	}

	// Collect metrics
	metrics.ProcessingTime = time.Since(startTime)
	metrics.TotalTransactions = int32(txSubmitted)

	if metrics.ProcessingTime.Seconds() > 0 && !timedOut {
		metrics.ThroughputTPS = float64(metrics.ValidTransactions) /
			metrics.ProcessingTime.Seconds()
	}

	// Resource metrics
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	metrics.ResourceUsage.CPUUsage = getCPUUsage()
	metrics.ResourceUsage.MemoryUsage = float64(mem.Alloc) / 1024 / 1024
	metrics.ResourceUsage.NumGoroutines = int32(runtime.NumGoroutine())
	metrics.ResourceUsage.PeakMemory = mem.TotalAlloc

	// Print immediate results
	printResults(&metrics)

	return metrics
}

func generateTestTransactions(config TestConfig) []*core.Transaction {
	transactions := make([]*core.Transaction, config.TotalTransactions)
	keySpace := int(float64(config.TotalTransactions) * (1 - config.DependencyRatio))
	if keySpace < 1 {
		keySpace = 1
	}

	for i := 0; i < config.TotalTransactions; i++ {
		tx := &core.Transaction{
			ID:        i,
			ReadSet:   make(map[string]string),
			WriteSet:  make(map[string]string),
			Status:    "valid",
			Timestamp: time.Now(),
		}

		// Generate read/write sets based on dependency ratio
		numKeys := 1 + (i % 3) // 1-3 keys per transaction
		for j := 0; j < numKeys; j++ {
			key := fmt.Sprintf("key%d", j%keySpace)
			value := fmt.Sprintf("value%d", i)

			if j%2 == 0 {
				tx.ReadSet[key] = value
			} else {
				tx.WriteSet[key] = value
			}
		}

		transactions[i] = tx
	}

	return transactions
}

func printResults(metrics *core.DetailedMetrics) {
	fmt.Printf("\nTest Results:\n")
	fmt.Printf("Strategy: %s (Dependency Ratio: %.2f)\n",
		metrics.Strategy, metrics.DependencyRatio)
	fmt.Printf("Total Transactions: %d\n", metrics.TotalTransactions)
	fmt.Printf("Valid Transactions: %d\n", metrics.ValidTransactions)
	fmt.Printf("Invalid Transactions: %d\n", metrics.InvalidTransactions)
	fmt.Printf("Resimulated Transactions: %d\n", metrics.ReSimulatedTransactions)
	fmt.Printf("Processing Time: %v\n", metrics.ProcessingTime)
	fmt.Printf("Throughput: %.2f TPS\n", metrics.ThroughputTPS)
	fmt.Printf("Memory Usage: %.2f MB\n", metrics.ResourceUsage.MemoryUsage)
	fmt.Printf("Active Goroutines: %d\n", metrics.ResourceUsage.NumGoroutines)
}

func getCPUUsage() float64 {
	// Placeholder for CPU usage calculation
	return 0.0
}
