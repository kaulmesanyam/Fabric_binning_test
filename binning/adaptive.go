// binning/adaptive.go
package binning

import (
	"sync"
)

type AdaptiveBinSizeController struct {
	BaseBinSize     int
	TransactionRate float64
	NetworkLatency  float64
	ContentionLevel float64
	mutex           sync.Mutex
}

func NewAdaptiveBinSizeController(baseBinSize int) *AdaptiveBinSizeController {
	return &AdaptiveBinSizeController{
		BaseBinSize: baseBinSize,
	}
}

func (c *AdaptiveBinSizeController) UpdateMetrics(txRate, netLatency, contention float64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.TransactionRate = txRate
	c.NetworkLatency = netLatency
	c.ContentionLevel = contention
}

func (c *AdaptiveBinSizeController) CalculateBinSize() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Adjust bin size based on metrics
	adjustedSize := float64(c.BaseBinSize) *
		(1 - c.ContentionLevel) *
		(c.TransactionRate / (1 + c.NetworkLatency))

	// Enforce minimum and maximum sizes
	minSize := c.BaseBinSize / 2
	maxSize := c.BaseBinSize * 2

	if adjustedSize < float64(minSize) {
		return minSize
	}
	if adjustedSize > float64(maxSize) {
		return maxSize
	}

	return int(adjustedSize)
}
