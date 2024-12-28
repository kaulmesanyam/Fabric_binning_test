package core

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Barrier provides synchronization for multiple goroutines
type Barrier struct {
	count      int32
	size       int32
	generation int32
	mutex      sync.Mutex
	cond       *sync.Cond
	done       chan struct{}
	timeout    time.Duration
}

func NewBarrier(size int) *Barrier {
	b := &Barrier{
		size:    int32(size),
		done:    make(chan struct{}),
		timeout: time.Millisecond * 500,
	}
	b.cond = sync.NewCond(&b.mutex)
	return b
}

func (b *Barrier) Wait() bool {
	b.mutex.Lock()
	generation := atomic.LoadInt32(&b.generation)
	count := atomic.AddInt32(&b.count, 1)

	if count == b.size {
		// Last goroutine to arrive
		atomic.StoreInt32(&b.count, 0)
		atomic.AddInt32(&b.generation, 1)
		close(b.done)
		b.done = make(chan struct{})
		b.cond.Broadcast()
		b.mutex.Unlock()
		return true
	}
	b.mutex.Unlock()

	// Wait with timeout
	select {
	case <-b.done:
		return true
	case <-time.After(b.timeout):
		fmt.Printf("Barrier wait timeout (gen %d, count %d/%d)\n",
			generation, count, b.size)
		b.Reset() // Force reset on timeout
		return false
	}
}

func (b *Barrier) Reset() {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	atomic.StoreInt32(&b.count, 0)
	atomic.AddInt32(&b.generation, 1)
	select {
	case <-b.done:
		// Already closed
	default:
		close(b.done)
	}
	b.done = make(chan struct{})
}

// SafeSendTransaction attempts to send a transaction with timeout
func SafeSendTransaction(ch chan *Transaction, tx *Transaction) bool {
	select {
	case ch <- tx:
		return true
	case <-time.After(time.Millisecond * 100):
		return false
	}
}

// SafeReceiveTransaction attempts to receive a transaction with timeout
func SafeReceiveTransaction(ch chan *Transaction) (*Transaction, bool) {
	select {
	case tx, ok := <-ch:
		return tx, ok
	case <-time.After(time.Millisecond * 100):
		return nil, false
	}
}

// SafeSendBin attempts to send a bin with timeout
func SafeSendBin(ch chan *Bin, bin *Bin) bool {
	select {
	case ch <- bin:
		return true
	case <-time.After(time.Millisecond * 100):
		return false
	}
}

// SafeReceiveBin attempts to receive a bin with timeout
func SafeReceiveBin(ch chan *Bin) (*Bin, bool) {
	select {
	case bin, ok := <-ch:
		return bin, ok
	case <-time.After(time.Millisecond * 100):
		return nil, false
	}
}

// SafeSendBlock attempts to send a block with timeout
func SafeSendBlock(ch chan *Block, block *Block) bool {
	select {
	case ch <- block:
		return true
	case <-time.After(time.Millisecond * 100):
		return false
	}
}

// SafeReceiveBlock attempts to receive a block with timeout
func SafeReceiveBlock(ch chan *Block) (*Block, bool) {
	select {
	case block, ok := <-ch:
		return block, ok
	case <-time.After(time.Millisecond * 100):
		return nil, false
	}
}

// BuildDAG constructs a DAG from transactions
func BuildDAG(transactions []*Transaction) []*DAGNode {
	nodes := make([]*DAGNode, len(transactions))
	for i, tx := range transactions {
		nodes[i] = &DAGNode{
			Transaction: tx,
			Processed:   false,
		}
	}

	// Build dependencies
	for i, node := range nodes {
		for j, otherNode := range nodes {
			if i != j && HasConflict(node.Transaction, otherNode.Transaction) {
				node.Dependencies = append(node.Dependencies, otherNode)
			}
		}
	}

	return nodes
}

// HasConflict checks for conflicts between transactions
func HasConflict(tx1, tx2 *Transaction) bool {
	// Check read-write conflicts
	for key := range tx1.ReadSet {
		if _, exists := tx2.WriteSet[key]; exists {
			return true
		}
	}

	// Check write-write conflicts
	for key := range tx1.WriteSet {
		if _, exists := tx2.WriteSet[key]; exists {
			return true
		}
		if _, exists := tx2.ReadSet[key]; exists {
			return true
		}
	}
	return false
}

// DAGLevels organizes DAG nodes into levels
func DAGLevels(nodes []*DAGNode) [][]*DAGNode {
	var levels [][]*DAGNode
	remaining := make(map[*DAGNode]bool)
	for _, node := range nodes {
		remaining[node] = true
	}

	for len(remaining) > 0 {
		var currentLevel []*DAGNode
		for node := range remaining {
			hasDep := false
			for _, dep := range node.Dependencies {
				if remaining[dep] {
					hasDep = true
					break
				}
			}
			if !hasDep {
				currentLevel = append(currentLevel, node)
				delete(remaining, node)
			}
		}

		// Handle cycles by picking any remaining node
		if len(currentLevel) == 0 && len(remaining) > 0 {
			for node := range remaining {
				currentLevel = append(currentLevel, node)
				delete(remaining, node)
				break
			}
		}

		levels = append(levels, currentLevel)
	}

	return levels
}
