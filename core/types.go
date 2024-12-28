package core

import (
	"sync"
	"sync/atomic"
	"time"
)

// SystemStatus tracks system state
type SystemStatus struct {
	IsClosing bool
	IsClosed  bool
	Mutex     sync.RWMutex
}

// Transaction represents a blockchain transaction
type Transaction struct {
	ID        int
	ReadSet   map[string]string
	WriteSet  map[string]string
	Status    string // "valid", "invalid", "resimulated"
	Timestamp time.Time
}

// Bin represents a group of transactions
type Bin struct {
	ID           int
	Transactions []*Transaction
	CreatedAt    time.Time
	Mutex        sync.RWMutex
	IsProcessed  bool
}

// Block represents a block of transactions
type Block struct {
	Number       int
	Bins         []*Bin
	Transactions []*Transaction
	Timestamp    time.Time
	IsProcessed  bool
	Mutex        sync.RWMutex
}

// DAGNode represents a node in dependency graph
type DAGNode struct {
	Transaction  *Transaction
	Dependencies []*DAGNode
	Processed    bool
	Mutex        sync.RWMutex
}

// ResourceMetrics tracks system resource usage
type ResourceMetrics struct {
	CPUUsage      float64
	MemoryUsage   float64
	NumGoroutines int32
	PeakMemory    uint64
}

// DetailedMetrics stores comprehensive test metrics
type DetailedMetrics struct {
	Strategy                string
	DependencyRatio         float64
	TotalTransactions       int32
	ValidTransactions       int32
	InvalidTransactions     int32
	ReSimulatedTransactions int32
	ProcessingTime          time.Duration
	ThroughputTPS           float64
	AverageBinSize          float64
	BinUtilization          float64
	AverageLatency          time.Duration
	MVCCConflicts           int32
	ResourceUsage           ResourceMetrics
	BinCreationTime         time.Duration
	CommitTime              time.Duration
	Mutex                   sync.RWMutex
}

func (m *DetailedMetrics) IncrementValid() {
	atomic.AddInt32(&m.ValidTransactions, 1)
}

func (m *DetailedMetrics) IncrementInvalid() {
	atomic.AddInt32(&m.InvalidTransactions, 1)
}

func (m *DetailedMetrics) IncrementResimulated() {
	atomic.AddInt32(&m.ReSimulatedTransactions, 1)
}

func (m *DetailedMetrics) AddMVCCConflict() {
	atomic.AddInt32(&m.MVCCConflicts, 1)
}

// ScenarioResult stores results for both strategies
type ScenarioResult struct {
	DependencyRatio    float64
	IndependentMetrics DetailedMetrics
	DependentMetrics   DetailedMetrics
}

// WorldState represents the current state
type WorldState struct {
	State map[string]string
	Mutex sync.RWMutex
}

// WorkerPool manages a pool of worker goroutines
type WorkerPool struct {
	workers chan struct{}
	wg      sync.WaitGroup
	size    int
}

func NewWorkerPool(size int) *WorkerPool {
	return &WorkerPool{
		workers: make(chan struct{}, size),
		size:    size,
	}
}

func (p *WorkerPool) Submit(work func()) bool {
	select {
	case p.workers <- struct{}{}:
		p.wg.Add(1)
		go func() {
			defer func() {
				<-p.workers
				p.wg.Done()
			}()
			work()
		}()
		return true
	case <-time.After(time.Millisecond * 100):
		return false
	}
}

func (p *WorkerPool) Wait() {
	p.wg.Wait()
}
