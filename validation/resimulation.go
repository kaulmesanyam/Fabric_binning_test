package validation

import (
	"fabric_binning_test/core"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
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
	TotalResimulated  int32
	SuccessfulResim   int32
	FailedResim       int32
	ReSimulationTime  time.Duration
	ValidationTime    time.Duration
	ProcessingLatency time.Duration
	mutex             sync.RWMutex
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

	select {
	case <-processComplete:
		fmt.Println("ReSimulationManager: Processing complete")
	case <-done:
		fmt.Println("ReSimulationManager: Shutdown initiated")
	}

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
			tx.Status = "resimulating"

			if err := rm.handleTransaction(tx, done); err != nil {
				fmt.Printf("Worker %d: Error handling transaction %d: %v\n", workerID, tx.ID, err)
				tx.Status = "resimulation_failed"
				atomic.AddInt32(&rm.metrics.FailedResim, 1)
			} else {
				processingTime := time.Since(startTime)
				fmt.Printf("Worker %d: Resimulated transaction %d in %v\n",
					workerID, tx.ID, processingTime)

				rm.metrics.mutex.Lock()
				rm.metrics.ProcessingLatency += processingTime
				rm.metrics.mutex.Unlock()

				tx.Status = "resimulation_success"
				atomic.AddInt32(&rm.metrics.SuccessfulResim, 1)
			}

		case <-done:
			return
		}
	}
}

func (rm *ReSimulationManager) handleTransaction(tx *core.Transaction, done chan struct{}) error {
	startTime := time.Now()
	atomic.AddInt32(&rm.metrics.TotalResimulated, 1)

	defer func() {
		rm.metrics.mutex.Lock()
		rm.metrics.ProcessingLatency += time.Since(startTime)
		rm.metrics.mutex.Unlock()
	}()

	// Resimulate with timeout
	resimResult := make(chan *core.Transaction, 1)
	errChan := make(chan error, 1)

	go func() {
		validationStart := time.Now()
		if resimulatedTx, err := rm.reSimulateTransaction(tx); err != nil {
			errChan <- err
		} else {
			resimResult <- resimulatedTx
		}
		rm.metrics.mutex.Lock()
		rm.metrics.ValidationTime += time.Since(validationStart)
		rm.metrics.mutex.Unlock()
	}()

	var resimulatedTx *core.Transaction

	select {
	case tx := <-resimResult:
		resimulatedTx = tx
	case err := <-errChan:
		return fmt.Errorf("resimulation error: %v", err)
	case <-done:
		return fmt.Errorf("resimulation cancelled")
	case <-time.After(time.Millisecond * 500):
		return fmt.Errorf("resimulation timeout")
	}

	// Validate resimulated transaction
	if !rm.validateResimulation(resimulatedTx) {
		return fmt.Errorf("validation failed for resimulated transaction")
	}

	// Create new bin for resimulated transaction
	bin := &core.Bin{
		ID:           resimulatedTx.ID,
		Transactions: []*core.Transaction{resimulatedTx},
		CreatedAt:    time.Now(),
	}

	// Try to send bin back to ordering service
	rm.status.Mutex.RLock()
	isClosing := rm.status.IsClosing
	rm.status.Mutex.RUnlock()

	if !isClosing {
		select {
		case rm.BinChannel <- bin:
			return nil
		case <-done:
			return fmt.Errorf("send cancelled")
		case <-time.After(time.Millisecond * 100):
			return fmt.Errorf("send timeout")
		}
	}

	return fmt.Errorf("manager is closing")
}

func (rm *ReSimulationManager) reSimulateTransaction(tx *core.Transaction) (*core.Transaction, error) {
	resimStart := time.Now()
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

	rm.metrics.mutex.Lock()
	rm.metrics.ReSimulationTime += time.Since(resimStart)
	rm.metrics.mutex.Unlock()

	return newTx, nil
}

func (rm *ReSimulationManager) validateResimulation(tx *core.Transaction) bool {
	validationStart := time.Now()
	rm.worldState.Mutex.RLock()
	defer rm.worldState.Mutex.RUnlock()

	success := true
	for key, expectedValue := range tx.ReadSet {
		if currentValue, exists := rm.worldState.State[key]; exists {
			if currentValue != expectedValue {
				success = false
				break
			}
		}
	}

	rm.metrics.mutex.Lock()
	rm.metrics.ValidationTime += time.Since(validationStart)
	rm.metrics.mutex.Unlock()

	return success
}

// Getter methods for metrics
func (rm *ReSimulationManager) GetTotalResimulated() int {
	return int(atomic.LoadInt32(&rm.metrics.TotalResimulated))
}

func (rm *ReSimulationManager) GetSuccessfulResimulations() int {
	return int(atomic.LoadInt32(&rm.metrics.SuccessfulResim))
}

func (rm *ReSimulationManager) GetFailedResimulations() int {
	return int(atomic.LoadInt32(&rm.metrics.FailedResim))
}

func (rm *ReSimulationManager) GetMetrics() ResimulationMetrics {
	rm.metrics.mutex.RLock()
	defer rm.metrics.mutex.RUnlock()
	return *rm.metrics
}
