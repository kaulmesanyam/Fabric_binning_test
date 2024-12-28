package committer

import (
	"fabric_binning_test/core"
	"fmt"
	"runtime"
	"sync"
	"time"
)

type ConcurrentCommitter struct {
	BlockChannel chan *core.Block
	ReSimChannel chan *core.Transaction
	Strategy     string
	numWorkers   int
	worldState   *core.WorldState
	metrics      *CommitMetrics
	status       *core.SystemStatus
	workerPool   *core.WorkerPool
	wg           sync.WaitGroup
}

type CommitMetrics struct {
	ValidTxCount      int32
	InvalidTxCount    int32
	ResimulateTxCount int32
	CommitTime        time.Duration
	ValidationTime    time.Duration
	mutex             sync.RWMutex
}

func NewCommitter(strategy string, metrics *core.DetailedMetrics) *ConcurrentCommitter {
	return &ConcurrentCommitter{
		BlockChannel: make(chan *core.Block, 1000),
		ReSimChannel: make(chan *core.Transaction, 1000),
		Strategy:     strategy,
		numWorkers:   runtime.NumCPU(),
		worldState: &core.WorldState{
			State: make(map[string]string),
		},
		metrics:    &CommitMetrics{},
		status:     &core.SystemStatus{},
		workerPool: core.NewWorkerPool(runtime.NumCPU()),
	}
}

func (cc *ConcurrentCommitter) Start(done chan struct{}) {
	fmt.Printf("Starting Committer with strategy: %s\n", cc.Strategy)

	processComplete := make(chan struct{})

	go func() {
		defer close(processComplete)

		for {
			select {
			case block, ok := <-cc.BlockChannel:
				if !ok {
					return
				}

				if cc.status.IsClosing {
					return
				}

				startTime := time.Now()
				fmt.Printf("Committer: Processing block %d with %d transactions\n",
					block.Number, len(block.Transactions))

				if cc.Strategy == "independent" {
					cc.processIndependentBlock(block, done)
				} else {
					cc.processDependentBlock(block, done)
				}

				cc.metrics.mutex.Lock()
				cc.metrics.CommitTime += time.Since(startTime)
				cc.metrics.mutex.Unlock()

				completed := make(chan struct{})
				go func() {
					cc.wg.Wait()
					close(completed)
				}()

				select {
				case <-completed:
					fmt.Printf("Committer: Completed block %d\n", block.Number)
				case <-time.After(time.Second):
					fmt.Printf("Committer: Timeout processing block %d\n", block.Number)
				case <-done:
					return
				}

			case <-done:
				fmt.Println("Committer: Received shutdown signal")
				return
			}
		}
	}()

	select {
	case <-processComplete:
		fmt.Println("Committer: Processing complete")
	case <-done:
		fmt.Println("Committer: Shutdown initiated")
	}

	cc.status.Mutex.Lock()
	cc.status.IsClosing = true
	if !cc.status.IsClosed {
		close(cc.ReSimChannel)
		cc.status.IsClosed = true
	}
	cc.status.Mutex.Unlock()

	cc.workerPool.Wait()
	cc.wg.Wait()
}

func (cc *ConcurrentCommitter) processIndependentBlock(block *core.Block, done chan struct{}) {
	totalTx := len(block.Transactions)
	barrier := core.NewBarrier(cc.numWorkers)

	for i := 0; i < cc.numWorkers; i++ {
		cc.wg.Add(1)
		workerID := i

		if !cc.workerPool.Submit(func() {
			defer cc.wg.Done()

			for j := workerID; j < totalTx; j += cc.numWorkers {
				select {
				case <-done:
					return
				default:
					tx := block.Transactions[j]
					validationStart := time.Now()

					if cc.validateAndCommit(tx) {
						cc.metrics.mutex.Lock()
						cc.metrics.ValidationTime += time.Since(validationStart)
						cc.metrics.mutex.Unlock()
						fmt.Printf("Committed transaction in %v\n", time.Since(tx.Timestamp))
					}
				}
			}

			if !barrier.Wait() {
				fmt.Println("Barrier wait timeout in independent block processing")
				return
			}
		}) {
			cc.wg.Done()
			fmt.Printf("Failed to submit worker for independent block processing\n")
		}
	}
}

func (cc *ConcurrentCommitter) processDependentBlock(block *core.Block, done chan struct{}) {
	cc.wg.Add(1)

	if !cc.workerPool.Submit(func() {
		defer cc.wg.Done()

		for _, bin := range block.Bins {
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

						if !cc.workerPool.Submit(func() {
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

							validationStart := time.Now()
							if cc.validateAndCommit(node.Transaction) {
								cc.metrics.mutex.Lock()
								cc.metrics.ValidationTime += time.Since(validationStart)
								cc.metrics.mutex.Unlock()

								node.Mutex.Lock()
								node.Processed = true
								node.Mutex.Unlock()
							}

							barrier.Wait()
						}) {
							levelWg.Done()
							fmt.Printf("Failed to submit DAG node processing\n")
						}
					}
				}
				levelWg.Wait()
			}
		}
	}) {
		cc.wg.Done()
		fmt.Printf("Failed to submit dependent block processing\n")
	}
}

func (cc *ConcurrentCommitter) validateAndCommit(tx *core.Transaction) bool {
	startTime := time.Now()

	defer func() {
		cc.metrics.mutex.Lock()
		cc.metrics.CommitTime += time.Since(startTime)
		cc.metrics.mutex.Unlock()
	}()

	// Validate transaction
	if !cc.validateTransaction(tx) {
		cc.metrics.mutex.Lock()
		cc.metrics.InvalidTxCount++
		cc.metrics.mutex.Unlock()

		// Send for resimulation
		select {
		case cc.ReSimChannel <- tx:
			tx.Status = "invalid"
			cc.metrics.mutex.Lock()
			cc.metrics.ResimulateTxCount++
			cc.metrics.mutex.Unlock()
		case <-time.After(time.Millisecond * 100):
			fmt.Printf("Timeout sending transaction %d for resimulation\n", tx.ID)
		}
		return false
	}

	// Commit transaction
	if err := cc.commitTransaction(tx); err != nil {
		cc.metrics.mutex.Lock()
		cc.metrics.InvalidTxCount++
		cc.metrics.mutex.Unlock()
		tx.Status = "invalid"
		return false
	}

	tx.Status = "valid"
	cc.metrics.mutex.Lock()
	cc.metrics.ValidTxCount++
	cc.metrics.mutex.Unlock()
	return true
}

func (cc *ConcurrentCommitter) validateTransaction(tx *core.Transaction) bool {
	cc.worldState.Mutex.RLock()
	defer cc.worldState.Mutex.RUnlock()

	for key, expectedValue := range tx.ReadSet {
		if currentValue, exists := cc.worldState.State[key]; exists {
			if currentValue != expectedValue {
				fmt.Printf("Validation failed for transaction %d: readset mismatch\n", tx.ID)
				return false
			}
		}
	}
	return true
}

func (cc *ConcurrentCommitter) commitTransaction(tx *core.Transaction) error {
	cc.worldState.Mutex.Lock()
	defer cc.worldState.Mutex.Unlock()

	select {
	case <-time.After(time.Millisecond * 10): // Simulate commit time
		for key, value := range tx.WriteSet {
			cc.worldState.State[key] = value
		}
		return nil
	case <-time.After(time.Millisecond * 100):
		return fmt.Errorf("commit timeout for transaction %d", tx.ID)
	}
}

// Getter methods for metrics
func (cc *ConcurrentCommitter) GetValidCount() int {
	cc.metrics.mutex.RLock()
	defer cc.metrics.mutex.RUnlock()
	return int(cc.metrics.ValidTxCount)
}

func (cc *ConcurrentCommitter) GetInvalidCount() int {
	cc.metrics.mutex.RLock()
	defer cc.metrics.mutex.RUnlock()
	return int(cc.metrics.InvalidTxCount)
}

func (cc *ConcurrentCommitter) GetResimulationCount() int {
	cc.metrics.mutex.RLock()
	defer cc.metrics.mutex.RUnlock()
	return int(cc.metrics.ResimulateTxCount)
}
