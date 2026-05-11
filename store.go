package zcache

import (
	"sync"
	"time"
)

// entry 是缓存中的单个条目。expiresAt 零值表示永不过期。
type entry struct {
	value     any
	expiresAt time.Time
}

var (
	mu    sync.RWMutex
	items = map[string]entry{}
)

// isExpired 判断 entry 是否已过期。
func isExpired(e entry, now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// Set 写入键值对。ttl <= 0 表示永不过期。
func Set(key string, value any, ttl time.Duration) {
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	mu.Lock()
	items[key] = entry{value: value, expiresAt: expiresAt}
	mu.Unlock()
}

// Has 判断当前缓存中是否存在指定 key。不触发 loader，不计入命中率。
func Has(key string) bool {
	mu.RLock()
	e, ok := items[key]
	mu.RUnlock()
	return ok && !isExpired(e, time.Now())
}

// Keys 返回当前所有未过期 key 的快照。顺序不保证。
func Keys() []string {
	now := time.Now()
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(items))
	for k, e := range items {
		if !isExpired(e, now) {
			out = append(out, k)
		}
	}
	return out
}

// Len 返回当前未过期的条目总数。
func Len() int {
	now := time.Now()
	mu.RLock()
	defer mu.RUnlock()
	n := 0
	for _, e := range items {
		if !isExpired(e, now) {
			n++
		}
	}
	return n
}

// Delete 强行删除一到多个 key。
func Delete(keys ...string) {
	if len(keys) == 0 {
		return
	}
	mu.Lock()
	for _, k := range keys {
		delete(items, k)
	}
	mu.Unlock()
}

// === janitor：周期性清理过期条目 ===

// janitorTick 是清理周期。设为 var 便于测试覆盖。
var janitorTick = time.Minute

var (
	janitorDone    chan struct{}
	janitorStopped chan struct{}
)

func startJanitor() {
	if janitorDone != nil {
		return // 已启动，幂等
	}
	janitorDone = make(chan struct{})
	janitorStopped = make(chan struct{})
	go janitorLoop(janitorTick, janitorDone, janitorStopped)
}

// stopJanitor 同步等待 janitor 协程退出，避免协程泄漏与与后续操作的时序竞争。
func stopJanitor() {
	if janitorDone == nil {
		return
	}
	close(janitorDone)
	<-janitorStopped
	janitorDone = nil
	janitorStopped = nil
}

func janitorLoop(tick time.Duration, done <-chan struct{}, stopped chan<- struct{}) {
	defer close(stopped)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			cleanupExpired()
		}
	}
}

func cleanupExpired() {
	now := time.Now()
	mu.Lock()
	for k, e := range items {
		if isExpired(e, now) {
			delete(items, k)
		}
	}
	mu.Unlock()
}

// clearAll 清空所有缓存条目。Stop 时调用。
func clearAll() {
	mu.Lock()
	items = map[string]entry{}
	mu.Unlock()
}
