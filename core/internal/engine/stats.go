package engine

import (
	"github.com/metacubex/mihomo/tunnel/statistic"
)

// ConnectionCount 返回 (当前活跃连接数, 启动以来出现过的连接总数)。
//
// mihomo 只维护活跃连接表，没有「历史总数」计数器，所以历史数由本方法惰性统计：
// 每次调用把当前活跃 ID 与上次快照比对，新出现的 ID 让计数 +1。
// 调用频率低于连接生命周期时可能漏计（UI 是 1s 轮询，实践上够用）。
func (e *Engine) ConnectionCount() (now, history int64) {
	if !e.Running() {
		e.mu.RLock()
		defer e.mu.RUnlock()
		return 0, e.connHistory
	}

	ids := make([]string, 0, 64)
	statistic.DefaultManager.Range(func(c statistic.Tracker) bool {
		ids = append(ids, c.ID())
		return true
	})

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.seenConns == nil {
		e.seenConns = make(map[string]struct{}, len(ids))
	}
	live := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		live[id] = struct{}{}
		if _, seen := e.seenConns[id]; !seen {
			e.connHistory++
		}
	}
	e.seenConns = live
	return int64(len(ids)), e.connHistory
}

// Traffic 返回最近一秒的上行/下行字节数（mihomo 的 blip）。
func (e *Engine) Traffic() (up, down int64) {
	return statistic.DefaultManager.Now()
}

// Total 返回累计上/下行字节数。
func (e *Engine) Total() (up, down int64) {
	return statistic.DefaultManager.Total()
}

// Memory 返回 mihomo 统计的进程 RSS（字节）。
func (e *Engine) Memory() uint64 {
	return statistic.DefaultManager.Memory()
}
