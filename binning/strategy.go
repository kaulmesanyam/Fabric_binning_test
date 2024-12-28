package binning

import (
	"fabric_binning_test/core"
	"time"
)

type BinningStrategy interface {
	AddTransaction(*core.Bin, *core.Transaction)
	CreateBin([]*core.Transaction, int) *core.Bin
}

type IndependentBinStrategy struct{}

func (s *IndependentBinStrategy) AddTransaction(bin *core.Bin, tx *core.Transaction) {
	bin.Mutex.Lock()
	defer bin.Mutex.Unlock()

	isIndependent := true
	for _, binTx := range bin.Transactions {
		if core.HasConflict(tx, binTx) {
			isIndependent = false
			break
		}
	}

	if isIndependent {
		bin.Transactions = append(bin.Transactions, tx)
	}
}

func (s *IndependentBinStrategy) CreateBin(transactions []*core.Transaction, maxSize int) *core.Bin {
	bin := &core.Bin{
		ID:        time.Now().Nanosecond(),
		CreatedAt: time.Now(),
	}

	added := 0
	for _, tx := range transactions {
		if added >= maxSize {
			break
		}

		bin.Mutex.Lock()
		isIndependent := true
		for _, binTx := range bin.Transactions {
			if core.HasConflict(tx, binTx) {
				isIndependent = false
				break
			}
		}

		if isIndependent {
			bin.Transactions = append(bin.Transactions, tx)
			added++
		}
		bin.Mutex.Unlock()
	}

	return bin
}

type DependentBinStrategy struct{}

func (s *DependentBinStrategy) AddTransaction(bin *core.Bin, tx *core.Transaction) {
	bin.Mutex.Lock()
	defer bin.Mutex.Unlock()

	hasDependent := false
	if len(bin.Transactions) > 0 {
		for _, binTx := range bin.Transactions {
			if core.HasConflict(tx, binTx) {
				hasDependent = true
				break
			}
		}
	}

	if hasDependent || len(bin.Transactions) == 0 {
		bin.Transactions = append(bin.Transactions, tx)
	}
}

func (s *DependentBinStrategy) CreateBin(transactions []*core.Transaction, maxSize int) *core.Bin {
	bin := &core.Bin{
		ID:        time.Now().Nanosecond(),
		CreatedAt: time.Now(),
	}

	processed := make(map[int]bool)
	added := 0

	for i, tx := range transactions {
		if processed[i] || added >= maxSize {
			continue
		}

		bin.Mutex.Lock()
		bin.Transactions = append(bin.Transactions, tx)
		processed[i] = true
		added++

		// Find dependent transactions
		for j := i + 1; j < len(transactions) && added < maxSize; j++ {
			if !processed[j] && core.HasConflict(tx, transactions[j]) {
				bin.Transactions = append(bin.Transactions, transactions[j])
				processed[j] = true
				added++
			}
		}
		bin.Mutex.Unlock()
	}

	return bin
}
