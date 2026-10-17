// Package clock 抽象时间来源：业务代码不得直接调用 time.Now，
// 这样阶段一后台的 /dev/clock 可以整体拨动时钟做“套餐到期/订单过期/奖励释放”演示。
package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type Real struct{}

func (Real) Now() time.Time { return time.Now().UTC() }

// Adjustable 只用于非生产环境。
type Adjustable struct {
	mu     sync.RWMutex
	offset time.Duration
}

func (a *Adjustable) Now() time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return time.Now().UTC().Add(a.offset)
}

func (a *Adjustable) SetOffset(d time.Duration) {
	a.mu.Lock()
	a.offset = d
	a.mu.Unlock()
}
