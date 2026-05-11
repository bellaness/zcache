package zcache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Loader 是缓存未命中时的加载函数。
// ctx 由触发加载的 Get 调用方传入；多并发触发时，loader 实际只执行一次，
// 此时 ctx 来自首个进入加载的调用方（singleflight 语义）。
type Loader func(ctx context.Context, key string) (value any, err error)

// loaderRule 是一条已注册的 loader。
type loaderRule struct {
	pattern string
	parts   []string
	ttl     time.Duration
	loader  Loader
	score   [3]int
	seq     uint64
}

var (
	loadersMu sync.RWMutex
	loaders   []loaderRule
	loaderSeq uint64

	sf singleflight.Group
)

// RegisterLoader 为符合 pattern 的 key 注册加载函数。
//
// pattern 与 DeletePattern 同语义；ttl <= 0 表示加载到的值永不过期。
// 同一 pattern 重复注册会覆盖原条目（保留原注册序号，不影响优先级）。
//
// 多 loader 同时匹配一个 key 时，按"最具体优先"选择：
// 字面段多的胜出；其次 * 多的胜出（* 比 ** 更严格）；再其次 ** 少的胜出；
// 仍平局则按注册顺序，先注册的胜出。
func RegisterLoader(pattern string, ttl time.Duration, loader Loader) {
	parts := splitParts(pattern)
	rule := loaderRule{
		pattern: pattern,
		parts:   parts,
		ttl:     ttl,
		loader:  loader,
		score:   computeScore(parts),
	}

	loadersMu.Lock()
	defer loadersMu.Unlock()

	for i := range loaders {
		if loaders[i].pattern == pattern {
			rule.seq = loaders[i].seq
			loaders[i] = rule
			sortLoadersLocked()
			return
		}
	}

	loaderSeq++
	rule.seq = loaderSeq
	loaders = append(loaders, rule)
	sortLoadersLocked()
}

// sortLoadersLocked 必须在 loadersMu 写锁内调用。
// 排序规则：score 降序、seq 升序。条目通常 < 100，插入排序足矣。
func sortLoadersLocked() {
	for i := 1; i < len(loaders); i++ {
		cur := loaders[i]
		j := i - 1
		for j >= 0 && lessLoader(cur, loaders[j]) {
			loaders[j+1] = loaders[j]
			j--
		}
		loaders[j+1] = cur
	}
}

// lessLoader 返回 a 是否应排在 b 之前（即 a 比 b 更优）。
func lessLoader(a, b loaderRule) bool {
	for i := 0; i < 3; i++ {
		if a.score[i] != b.score[i] {
			return a.score[i] > b.score[i]
		}
	}
	return a.seq < b.seq
}

// findLoader 返回 key 匹配的最优 loader。
func findLoader(key string) (loaderRule, bool) {
	keyParts := splitParts(key)
	loadersMu.RLock()
	defer loadersMu.RUnlock()
	for _, r := range loaders {
		if match(r.parts, keyParts) {
			return r, true
		}
	}
	return loaderRule{}, false
}

// loadAndStore 调用 loader 并将结果写入缓存。同 key 并发请求会被 singleflight 去重。
//
// 用 DoChan + select 而非 Do：让等待者能独立响应自身 ctx 的取消，
// 而不必等到 loader 完成；loader 协程仍在后台跑完，结果对其他未取消的等待者依然有效。
func loadAndStore(ctx context.Context, key string, rule loaderRule) (any, error) {
	ch := sf.DoChan(key, func() (any, error) {
		// 二次检查：可能在排队期间已被别的协程加载完
		mu.RLock()
		e, ok := items[key]
		mu.RUnlock()
		if ok && !isExpired(e, time.Now()) {
			return e.value, nil
		}
		val, err := rule.loader(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrLoaderFailed, err)
		}
		Set(key, val, rule.ttl)
		return val, nil
	})
	select {
	case res := <-ch:
		return res.Val, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// resetForTest 仅供测试使用：清空缓存条目和已注册的 loader。
//
// 注意：不重置 sf。singleflight.Group 的内部 map 在每次调用结束时会自行清理，
// 直接赋值置零会与上一测试残留的清理协程产生数据竞争。
func resetForTest() {
	mu.Lock()
	items = map[string]entry{}
	mu.Unlock()
	loadersMu.Lock()
	loaders = nil
	loaderSeq = 0
	loadersMu.Unlock()
}
