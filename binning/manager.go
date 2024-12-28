package binning

import (
	"fabric_binning_test/core"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type ConcurrentBinManager struct {
	TransactionPool chan *core.Transaction
	BinChannel      chan *core.Bin
	Strategy        string
	MaxBinSize      int
	MaxWaitTime     time.Duration
	CurrentBin      *core.Bin
	numWorkers      int
	binMutex        sync.RWMutex
	strategy        BinningStrategy
	workerPool      *core.WorkerPool
	status          *core.SystemStatus
	metrics         *core.DetailedMetrics
}

type BinningMetrics struct {
	BinsCreated        int32
	TotalTransactions  int32
	BinnedTransactions int32
	BinningTime        time.Duration
	AvgBinUtilization  float64
	mutex              sync.RWMutex
}

func NewBinManager(strategy string, maxBinSize int, metrics *core.DetailedMetrics) *ConcurrentBinManager {
	var binStrat BinningStrategy
	if strategy == "independent" {
		binStrat = &IndependentBinStrategy{}
	} else {
		binStrat = &DependentBinStrategy{}
	}

	return &ConcurrentBinManager{
		TransactionPool: make(chan *core.Transaction, 10000),
		BinChannel:      make(chan *core.Bin, 1000),
		Strategy:        strategy,
		MaxBinSize:      maxBinSize,
		MaxWaitTime:     time.Second * 1,
		numWorkers:      runtime.NumCPU(),
		strategy:        binStrat,
		workerPool:      core.NewWorkerPool(runtime.NumCPU()),
		status:          &core.SystemStatus{},
		metrics:         metrics,
	}
}

func (bm *ConcurrentBinManager) Start(done chan struct{}) {
	fmt.Printf("Starting BinManager with %d workers, strategy: %s\n",
		bm.numWorkers, bm.Strategy)

	var wg sync.WaitGroup
	shutdownChan := make(chan struct{})

	// Start bin finalizer
	wg.Add(1)
	go func() {
		defer wg.Done()
		bm.binFinalizer(shutdownChan)
	}()

	// Start transaction processing
	finishedProcessing := make(chan struct{})
	go func() {
		defer close(finishedProcessing)

		for {
			select {
			case tx, ok := <-bm.TransactionPool:
				if !ok {
					return
				}

				if bm.status.IsClosing {
					return
				}

				if !bm.workerPool.Submit(func() {
					bm.processSingleTransaction(tx)
				}) {
					fmt.Printf("Failed to submit transaction to worker pool\n")
					atomic.AddInt32(&bm.metrics.InvalidTransactions, 1)
				}

			case <-done:
				bm.status.Mutex.Lock()
				bm.status.IsClosing = true
				bm.status.Mutex.Unlock()
				return
			}
		}
	}()

	// Wait for processing completion or shutdown signal
	select {
	case <-finishedProcessing:
		fmt.Println("BinManager: Processing complete")
	case <-done:
		fmt.Println("BinManager: Received shutdown signal")
	}

	// Clean shutdown
	close(shutdownChan)
	bm.workerPool.Wait()
	wg.Wait()

	bm.status.Mutex.Lock()
	if !bm.status.IsClosed {
		close(bm.BinChannel)
		bm.status.IsClosed = true
	}
	bm.status.Mutex.Unlock()
}

func (bm *ConcurrentBinManager) processSingleTransaction(tx *core.Transaction) {
	startTime := time.Now()

	// Update transaction count
	atomic.AddInt32(&bm.metrics.TotalTransactions, 1)
	tx.Status = "processing"

	bm.binMutex.Lock()
	defer bm.binMutex.Unlock()

	if bm.status.IsClosing {
		return
	}

	// Initialize new bin if needed
	if bm.CurrentBin == nil {
		bm.CurrentBin = &core.Bin{
			ID:        time.Now().Nanosecond(),
			CreatedAt: time.Now(),
		}
	}

	// Track bin size before and after adding transaction
	beforeSize := len(bm.CurrentBin.Transactions)
	bm.strategy.AddTransaction(bm.CurrentBin, tx)
	afterSize := len(bm.CurrentBin.Transactions)

	// Update metrics based on binning result
	if afterSize > beforeSize {
		atomic.AddInt32(&bm.metrics.ValidTransactions, 1)
		tx.Status = "binned"

		// Update bin utilization metrics
		bm.metrics.Mutex.Lock()
		bm.metrics.BinUtilization = float64(afterSize) / float64(bm.MaxBinSize)
		bm.metrics.Mutex.Unlock()
	} else {
		atomic.AddInt32(&bm.metrics.InvalidTransactions, 1)
		tx.Status = "unbinned"
	}

	// Check if bin is full
	if len(bm.CurrentBin.Transactions) >= bm.MaxBinSize {
		bm.finalizeBin()
	}

	// Update timing metrics
	bm.metrics.Mutex.Lock()
	bm.metrics.BinCreationTime += time.Since(startTime)
	bm.metrics.AverageLatency += time.Since(tx.Timestamp)
	bm.metrics.Mutex.Unlock()
}

func (bm *ConcurrentBinManager) binFinalizer(shutdown chan struct{}) {
	ticker := time.NewTicker(bm.MaxWaitTime / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			bm.binMutex.Lock()
			if bm.CurrentBin != nil && time.Since(bm.CurrentBin.CreatedAt) >= bm.MaxWaitTime {
				if !bm.status.IsClosing {
					bm.finalizeBin()
				}
			}
			bm.binMutex.Unlock()

		case <-shutdown:
			bm.binMutex.Lock()
			if bm.CurrentBin != nil && len(bm.CurrentBin.Transactions) > 0 && !bm.status.IsClosing {
				bm.finalizeBin()
			}
			bm.binMutex.Unlock()
			return
		}
	}
}

func (bm *ConcurrentBinManager) finalizeBin() {
	if bm.CurrentBin != nil && len(bm.CurrentBin.Transactions) > 0 {
		bin := bm.CurrentBin
		bm.CurrentBin = nil

		// Update bin metrics
		atomic.AddInt32(&bm.metrics.TotalTransactions, int32(len(bin.Transactions)))

		bm.metrics.Mutex.Lock()
		bm.metrics.AverageBinSize = float64(len(bin.Transactions))
		binTime := time.Since(bin.CreatedAt)
		bm.metrics.BinCreationTime += binTime
		bm.metrics.Mutex.Unlock()

		// Check if system is closing before sending
		bm.status.Mutex.RLock()
		isClosing := bm.status.IsClosing
		bm.status.Mutex.RUnlock()

		if !isClosing {
			if core.SafeSendBin(bm.BinChannel, bin) {
				fmt.Printf("Finalized bin with %d transactions (creation time: %v)\n",
					len(bin.Transactions), binTime)

				// Update success metrics
				atomic.AddInt32(&bm.metrics.ValidTransactions, int32(len(bin.Transactions)))
			} else {
				fmt.Printf("Failed to send bin to channel\n")
				atomic.AddInt32(&bm.metrics.InvalidTransactions, int32(len(bin.Transactions)))
			}
		}
	}
}

// Metric retrieval methods
func (bm *ConcurrentBinManager) GetTotalTransactions() int {
	return int(atomic.LoadInt32(&bm.metrics.TotalTransactions))
}

func (bm *ConcurrentBinManager) GetValidTransactions() int {
	return int(atomic.LoadInt32(&bm.metrics.ValidTransactions))
}

func (bm *ConcurrentBinManager) GetInvalidTransactions() int {
	return int(atomic.LoadInt32(&bm.metrics.InvalidTransactions))
}

func (bm *ConcurrentBinManager) GetBinningMetrics() string {
	bm.metrics.Mutex.RLock()
	defer bm.metrics.Mutex.RUnlock()

	return fmt.Sprintf(
		"Binning Metrics:\n"+
			"Total Transactions: %d\n"+
			"Valid Transactions: %d\n"+
			"Invalid Transactions: %d\n"+
			"Average Bin Size: %.2f\n"+
			"Average Bin Utilization: %.2f%%\n"+
			"Average Binning Time: %v",
		atomic.LoadInt32(&bm.metrics.TotalTransactions),
		atomic.LoadInt32(&bm.metrics.ValidTransactions),
		atomic.LoadInt32(&bm.metrics.InvalidTransactions),
		bm.metrics.AverageBinSize,
		bm.metrics.BinUtilization*100,
		bm.metrics.BinCreationTime/time.Duration(atomic.LoadInt32(&bm.metrics.TotalTransactions)),
	)
}
