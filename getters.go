package zcache

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotFound 表示 key 在缓存中不存在且没有匹配的 loader。
	ErrNotFound = errors.New("zcache: key 不存在")
	// ErrTypeMismatch 表示缓存值的实际类型与 GetT 等请求的类型不一致。
	ErrTypeMismatch = errors.New("zcache: 缓存值类型不匹配")
	// ErrLoaderFailed 包装 loader 自身返回的错误。
	ErrLoaderFailed = errors.New("zcache: loader 执行失败")
)

// Get 读取 key 对应的值。
//   - 命中且未过期：直接返回，ctx 不被使用
//   - 未命中（含已过期被清理）且匹配到 loader：调用 loader，ctx 一路传入
//   - 未命中且无 loader：返回 ErrNotFound
//
// ctx 取消时，等待中的调用会立即返回 ctx.Err()；正在跑的 loader 仍会在后台完成。
func Get(ctx context.Context, key string) (any, error) {
	mu.RLock()
	e, ok := items[key]
	mu.RUnlock()
	if ok && !isExpired(e, time.Now()) {
		return e.value, nil
	}
	if ok {
		// 已过期：先清理掉，避免读到陈旧值
		Delete(key)
	}
	rule, ok := findLoader(key)
	if !ok {
		return nil, ErrNotFound
	}
	return loadAndStore(ctx, key, rule)
}

// GetT 是 Get 的范型版，自动做类型断言。
// 类型不一致时返回 ErrTypeMismatch。
func GetT[T any](ctx context.Context, key string) (T, error) {
	var zero T
	v, err := Get(ctx, key)
	if err != nil {
		return zero, err
	}
	t, ok := v.(T)
	if !ok {
		return zero, ErrTypeMismatch
	}
	return t, nil
}

// GetInt 等同 GetT[int]。
func GetInt(ctx context.Context, key string) (int, error) { return GetT[int](ctx, key) }

// GetString 等同 GetT[string]。
func GetString(ctx context.Context, key string) (string, error) { return GetT[string](ctx, key) }

// GetBool 等同 GetT[bool]。
func GetBool(ctx context.Context, key string) (bool, error) { return GetT[bool](ctx, key) }
