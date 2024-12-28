package validation

import (
	"fabric_binning_test/core"
	"fmt"
	"runtime"
	"sync"
	"time"
)

type ReSimulationManager struct {
	ReSimulationQueue chan *core.Transaction
	BinChannel        chan *core.Bin
	numWorkers        int
	worldState        *core.WorldState
	metrics           *ResimulationMetrics
	status            *core.SystemStatus
	workerPool        *core.WorkerPool
	wg                sync.WaitGroup
}

type ResimulationMetrics struct {
	TotalResimulated int32
	SuccessfulResim  int32
	FailedResim      int32
	mutex            sync.RWMutex
}

func NewReSimulationManager(worldState *core.WorldState, metrics *core.DetailedMetrics) *ReSimulationManager {
	return &ReSimulationManager{
		ReSimulationQueue: make(chan *core.Transaction, 1000),
		BinChannel:        make(chan *core.Bin, 100),
		numWorkers:        runtime.NumCPU(),
		worldState:        worldState,
		metrics:           &ResimulationMetrics{},
		status:            &core.SystemStatus{},
		workerPool:        core.NewWorkerPool(runtime.NumCPU()),
	}
}

func (rm *ReSimulationManager) Start(done chan struct{}) {
	fmt.Printf("Starting ReSimulationManager with %d workers\n", rm.numWorkers)

	processComplete := make(chan struct{})

	// Start processing goroutine
	go func() {
		defer close(processComplete)

		for i := 0; i < rm.numWorkers; i++ {
			rm.wg.Add(1)
			go func(workerID int) {
				defer rm.wg.Done()
				rm.processReSimulations(done, workerID)
			}(i)
		}

		rm.wg.Wait()
	}()

	// Wait for completion or done signal
	select {
	case <-processComplete:
		fmt.Println("ReSimulationManager: Processing complete")
	case <-done:
		fmt.Println("ReSimulationManager: Shutdown initiated")
	}

	// Cleanup
	rm.status.Mutex.Lock()
	rm.status.IsClosing = true
	rm.status.Mutex.Unlock()

	rm.workerPool.Wait()
	rm.wg.Wait()
}

func (rm *ReSimulationManager) processReSimulations(done chan struct{}, workerID int) {
	for {
		select {
		case tx, ok := <-rm.ReSimulationQueue:
			if !ok {
				return
			}

			rm.status.Mutex.RLock()
			if rm.status.IsClosing {
				rm.status.Mutex.RUnlock()
				return
			}
			rm.status.Mutex.RUnlock()

			startTime := time.Now()
			if err := rm.handleTransaction(tx, done); err != nil {
				fmt.Printf("Worker %d: Error handling transaction: %v\n", workerID, err)
			} else {
				fmt.Printf("Worker %d: Resimulated transaction in %v\n", workerID, time.Since(startTime))
			}

		case <-done:
			return
		}
	}
}

func (rm *ReSimulationManager) handleTransaction(tx *core.Transaction, done chan struct{}) error {
	rm.metrics.mutex.Lock()
	rm.metrics.TotalResimulated++
	rm.metrics.mutex.Unlock()

	// Resimulate with timeout
	resimResult := make(chan *core.Transaction, 1)
	errChan := make(chan error, 1)

	go func() {
		if resimulatedTx, err := rm.reSimulateTransaction(tx); err != nil {
			errChan <- err
		} else {
			resimResult <- resimulatedTx
		}
	}()

	var resimulatedTx *core.Transaction
	select {
	case tx := <-resimResult:
		resimulatedTx = tx
	case err := <-errChan:
		rm.updateMetrics(false)
		return err
	case <-done:
		return fmt.Errorf("resimulation cancelled")
	case <-time.After(time.Millisecond * 500):
		rm.updateMetrics(false)
		return fmt.Errorf("resimulation timeout")
	}

	// Validate resimulated transaction
	if !rm.validateResimulation(resimulatedTx) {
		rm.updateMetrics(false)
		return fmt.Errorf("validation failed")
	}

	// Create new bin
	bin := &core.Bin{
		ID:           resimulatedTx.ID,
		Transactions: []*core.Transaction{resimulatedTx},
		CreatedAt:    time.Now(),
	}

	// Try to send bin
	rm.status.Mutex.RLock()
	isClosing := rm.status.IsClosing
	rm.status.Mutex.RUnlock()

	if !isClosing {
		select {
		case rm.BinChannel <- bin:
			rm.updateMetrics(true)
			return nil
		case <-done:
			return fmt.Errorf("send cancelled")
		case <-time.After(time.Millisecond * 100):
			rm.updateMetrics(false)
			return fmt.Errorf("send timeout")
		}
	}

	return fmt.Errorf("manager is closing")
}

func (rm *ReSimulationManager) reSimulateTransaction(tx *core.Transaction) (*core.Transaction, error) {
	rm.worldState.Mutex.RLock()
	defer rm.worldState.Mutex.RUnlock()

	// Simulate processing time
	select {
	case <-time.After(time.Millisecond * 10):
	case <-time.After(time.Millisecond * 100):
		return nil, fmt.Errorf("processing timeout")
	}

	newTx := &core.Transaction{
		ID:        tx.ID,
		ReadSet:   make(map[string]string),
		WriteSet:  make(map[string]string),
		Status:    "resimulated",
		Timestamp: time.Now(),
	}

	// Update read set based on current world state
	for key := range tx.ReadSet {
		if value, exists := rm.worldState.State[key]; exists {
			newTx.ReadSet[key] = value
		}
	}

	// Keep original write operations
	for key, value := range tx.WriteSet {
		newTx.WriteSet[key] = value
	}

	return newTx, nil
}

func (rm *ReSimulationManager) validateResimulation(tx *core.Transaction) bool {
	rm.worldState.Mutex.RLock()
	defer rm.worldState.Mutex.RUnlock()

	for key, expectedValue := range tx.ReadSet {
		if currentValue, exists := rm.worldState.State[key]; exists {
			if currentValue != expectedValue {
				return false
			}
		}
	}
	return true
}

func (rm *ReSimulationManager) updateMetrics(success bool) {
	rm.metrics.mutex.Lock()
	defer rm.metrics.mutex.Unlock()

	if success {
		rm.metrics.SuccessfulResim++
	} else {
		rm.metrics.FailedResim++
	}
}

// Getter methods for metrics
func (rm *ReSimulationManager) GetTotalResimulated() int {
	rm.metrics.mutex.RLock()
	defer rm.metrics.mutex.RUnlock()
	return int(rm.metrics.TotalResimulated)
}

func (rm *ReSimulationManager) GetSuccessfulResimulations() int {
	rm.metrics.mutex.RLock()
	defer rm.metrics.mutex.RUnlock()
	return int(rm.metrics.SuccessfulResim)
}

func (rm *ReSimulationManager) GetFailedResimulations() int {
	rm.metrics.mutex.RLock()
	defer rm.metrics.mutex.RUnlock()
	return int(rm.metrics.FailedResim)
}

// Key fixes:
// 1. Better channel closing coordination
// 2. Proper handling of done signal
// 3. Fixed shutdown sequence
// 4. Added status checks before sending
// 5. Added timeout handling for all operations
// 6. Better error handling and reporting
// 7. Improved worker management
// 8. More robust cleanup sequence

// Try running the tests again with this updated version. The resimulation manager should handle shutdown more gracefully now.
