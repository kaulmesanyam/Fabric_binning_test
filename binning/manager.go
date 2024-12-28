package binning

import (
	"fabric_binning_test/core"
	"fmt"
	"runtime"
	"sync"
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
	fmt.Printf("Starting BinManager with %d workers, strategy: %s\n", bm.numWorkers, bm.Strategy)

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

				if !bm.workerPool.Submit(func() { bm.processSingleTransaction(tx) }) {
					fmt.Printf("Failed to submit transaction to worker pool\n")
				}

			case <-done:
				bm.status.Mutex.Lock()
				bm.status.IsClosing = true
				bm.status.Mutex.Unlock()
				return
			}
		}
	}()

	// Wait for processing to complete
	select {
	case <-finishedProcessing:
		fmt.Println("BinManager: Processing complete")
	case <-done:
		fmt.Println("BinManager: Received shutdown signal")
	}

	// Signal bin finalizer to stop
	close(shutdownChan)

	// Wait for all workers and finalizer to complete
	bm.workerPool.Wait()
	wg.Wait()

	// Close bin channel only after all workers are done
	bm.status.Mutex.Lock()
	if !bm.status.IsClosed {
		close(bm.BinChannel)
		bm.status.IsClosed = true
	}
	bm.status.Mutex.Unlock()
}

func (bm *ConcurrentBinManager) processSingleTransaction(tx *core.Transaction) {
	startTime := time.Now()

	bm.binMutex.Lock()
	defer bm.binMutex.Unlock()

	if bm.status.IsClosing {
		return
	}

	if bm.CurrentBin == nil {
		bm.CurrentBin = &core.Bin{
			ID:        time.Now().Nanosecond(),
			CreatedAt: time.Now(),
		}
	}

	// Add transaction using strategy
	bm.strategy.AddTransaction(bm.CurrentBin, tx)

	// Check if bin is full
	if len(bm.CurrentBin.Transactions) >= bm.MaxBinSize {
		bm.finalizeBin()
	}

	bm.metrics.BinCreationTime += time.Since(startTime)
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

		// Check if system is closing before sending
		bm.status.Mutex.RLock()
		isClosing := bm.status.IsClosing
		bm.status.Mutex.RUnlock()

		if !isClosing {
			if core.SafeSendBin(bm.BinChannel, bin) {
				fmt.Printf("Finalized bin with %d transactions\n", len(bin.Transactions))
			} else {
				fmt.Printf("Failed to send bin to channel\n")
			}
		}
	}
}
