package ordering

import (
	"fabric_binning_test/core"
	"fmt"
	"runtime"
	"sync"
	"time"
)

type ConcurrentOrderingService struct {
	BinChannel   chan *core.Bin
	BlockChannel chan *core.Block
	Strategy     string
	blockNumber  int32
	numWorkers   int
	mutex        sync.RWMutex
	status       *core.SystemStatus
	workerPool   *core.WorkerPool
	metrics      *core.DetailedMetrics
	wg           sync.WaitGroup
}

func NewOrderingService(strategy string, metrics *core.DetailedMetrics) *ConcurrentOrderingService {
	return &ConcurrentOrderingService{
		BinChannel:   make(chan *core.Bin, 1000),
		BlockChannel: make(chan *core.Block, 1000),
		Strategy:     strategy,
		numWorkers:   runtime.NumCPU(),
		status:       &core.SystemStatus{},
		workerPool:   core.NewWorkerPool(runtime.NumCPU()),
		metrics:      metrics,
	}
}

func (os *ConcurrentOrderingService) Start(done chan struct{}) {
	fmt.Printf("Starting OrderingService with strategy: %s\n", os.Strategy)

	processComplete := make(chan struct{})
	currentBlock := &core.Block{
		Number:    int(os.blockNumber),
		Timestamp: time.Now(),
	}

	// Start main processing loop
	go func() {
		defer close(processComplete)

		for {
			select {
			case bin, ok := <-os.BinChannel:
				if !ok {
					// Process remaining transactions and exit
					os.finalizePendingBlock(currentBlock, done)
					return
				}

				if os.status.IsClosing {
					return
				}

				startTime := time.Now()

				// Process bin based on strategy
				if os.Strategy == "independent" {
					os.processIndependentBin(bin, done)
				} else {
					os.processDependentBin(bin, done)
				}

				// Update metrics
				os.metrics.Mutex.Lock()
				os.metrics.AverageLatency += time.Since(startTime)
				os.metrics.Mutex.Unlock()

				// Add to current block
				os.mutex.Lock()
				currentBlock.Bins = append(currentBlock.Bins, bin)
				currentBlock.Transactions = append(currentBlock.Transactions, bin.Transactions...)

				// Check if block is ready
				if len(currentBlock.Transactions) >= 50 {
					if os.sendBlock(currentBlock, done) {
						currentBlock = &core.Block{
							Number:    int(os.blockNumber),
							Timestamp: time.Now(),
						}
					}
				}
				os.mutex.Unlock()

			case <-done:
				fmt.Println("OrderingService: Received shutdown signal")
				os.finalizePendingBlock(currentBlock, done)
				return
			}
		}
	}()

	// Wait for completion or done signal
	select {
	case <-processComplete:
		fmt.Println("OrderingService: Processing complete")
	case <-done:
		fmt.Println("OrderingService: Shutdown initiated")
	}

	// Cleanup
	os.status.Mutex.Lock()
	os.status.IsClosing = true
	if !os.status.IsClosed {
		close(os.BlockChannel)
		os.status.IsClosed = true
	}
	os.status.Mutex.Unlock()

	// Wait for all workers to complete
	os.workerPool.Wait()
	os.wg.Wait()
}

func (os *ConcurrentOrderingService) processIndependentBin(bin *core.Bin, done chan struct{}) {
	workers := min(len(bin.Transactions), os.numWorkers)
	if workers == 0 {
		return
	}

	barrier := core.NewBarrier(workers)

	for i := 0; i < workers; i++ {
		os.wg.Add(1)
		workerID := i

		if !os.workerPool.Submit(func() {
			defer os.wg.Done()

			for j := workerID; j < len(bin.Transactions); j += workers {
				select {
				case <-done:
					return
				default:
					os.processTransaction(bin.Transactions[j])
					if !barrier.Wait() {
						// Barrier timeout, abort processing
						return
					}
				}
			}
		}) {
			os.wg.Done()
			fmt.Printf("Failed to submit worker for independent bin processing\n")
		}
	}
}

func (os *ConcurrentOrderingService) processDependentBin(bin *core.Bin, done chan struct{}) {
	os.wg.Add(1)

	if !os.workerPool.Submit(func() {
		defer os.wg.Done()

		dag := core.BuildDAG(bin.Transactions)
		levels := core.DAGLevels(dag)

		for _, level := range levels {
			barrier := core.NewBarrier(len(level))
			var levelWg sync.WaitGroup

			for _, node := range level {
				select {
				case <-done:
					return
				default:
					levelWg.Add(1)

					if !os.workerPool.Submit(func() {
						defer levelWg.Done()

						// Wait for dependencies
						for _, dep := range node.Dependencies {
							timeout := time.After(time.Millisecond * 500)
							for !dep.Processed {
								select {
								case <-timeout:
									return
								case <-done:
									return
								case <-time.After(time.Millisecond):
									if dep.Processed {
										break
									}
								}
							}
						}

						os.processTransaction(node.Transaction)

						node.Mutex.Lock()
						node.Processed = true
						node.Mutex.Unlock()

						barrier.Wait()
					}) {
						levelWg.Done()
						fmt.Printf("Failed to submit DAG node processing\n")
					}
				}
			}
			levelWg.Wait()
		}
	}) {
		os.wg.Done()
		fmt.Printf("Failed to submit dependent bin processing\n")
	}
}

func (os *ConcurrentOrderingService) processTransaction(tx *core.Transaction) {
	// Simulate transaction processing
	select {
	case <-time.After(time.Millisecond * 10):
	case <-time.After(time.Millisecond * 100):
		fmt.Printf("Transaction processing timeout for tx %d\n", tx.ID)
	}
}

func (os *ConcurrentOrderingService) sendBlock(block *core.Block, done chan struct{}) bool {
	select {
	case os.BlockChannel <- block:
		fmt.Printf("OrderingService: Sent block %d with %d transactions\n",
			block.Number, len(block.Transactions))
		os.blockNumber++
		return true
	case <-done:
		return false
	case <-time.After(time.Millisecond * 500):
		fmt.Printf("Timeout sending block %d\n", block.Number)
		return false
	}
}

func (os *ConcurrentOrderingService) finalizePendingBlock(block *core.Block, done chan struct{}) {
	if block != nil && len(block.Transactions) > 0 {
		os.sendBlock(block, done)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
