package zcache

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSetGet_Basic(t *testing.T) {
	resetForTest()
	Set("a", 42, 0)
	v, err := Get(context.Background(), "a")
	if err != nil {
		t.Fatalf("Get returned err: %v", err)
	}
	if v != 42 {
		t.Errorf("got %v, want 42", v)
	}
}

func TestGet_MissNoLoader(t *testing.T) {
	resetForTest()
	_, err := Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestSet_TTLExpire(t *testing.T) {
	resetForTest()
	Set("a", 1, 30*time.Millisecond)
	if !Has("a") {
		t.Fatal("Has should be true immediately")
	}
	time.Sleep(60 * time.Millisecond)
	if Has("a") {
		t.Error("Has should be false after TTL")
	}
	_, err := Get(context.Background(), "a")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after expire = %v, want ErrNotFound", err)
	}
}

func TestSet_NeverExpire(t *testing.T) {
	resetForTest()
	Set("a", "x", 0)
	Set("b", "y", -1)
	time.Sleep(20 * time.Millisecond)
	if !Has("a") || !Has("b") {
		t.Error("ttl<=0 应永不过期")
	}
}

func TestDelete_Multiple(t *testing.T) {
	resetForTest()
	Set("a", 1, 0)
	Set("b", 2, 0)
	Set("c", 3, 0)
	Delete("a", "c")
	if Has("a") || Has("c") {
		t.Error("a/c 应已删除")
	}
	if !Has("b") {
		t.Error("b 不应被删除")
	}
}

func TestDeletePattern(t *testing.T) {
	resetForTest()
	Set("a", 1, 0)
	Set("a.b", 2, 0)
	Set("a.b.c", 3, 0)
	Set("a.x.c", 4, 0)
	Set("z.b.c", 5, 0)

	if n := DeletePattern("a.*.c"); n != 2 { // a.b.c, a.x.c
		t.Errorf("a.*.c 删除 %d, want 2", n)
	}
	if Has("a.b.c") || Has("a.x.c") {
		t.Error("a.b.c / a.x.c 未被删除")
	}
	if !Has("a") || !Has("a.b") || !Has("z.b.c") {
		t.Error("不应被 a.*.c 命中的 key 被误删")
	}

	// ** 含 0 段
	Set("k.v", 10, 0)
	Set("k.x.v", 11, 0)
	Set("k.x.y.v", 12, 0)
	if n := DeletePattern("k.**.v"); n != 3 {
		t.Errorf("k.**.v 删除 %d, want 3", n)
	}

	// 单 *
	Set("p", 100, 0)
	Set("p.q", 200, 0)
	if n := DeletePattern("*"); n != 2 { // a, p
		t.Errorf("'*' 删除 %d, want 2", n)
	}
	if !Has("p.q") || !Has("a.b") || !Has("z.b.c") {
		t.Error("'*' 应只命中单段 key")
	}

	// ** 删除全部
	if Len() == 0 {
		t.Fatal("前置：应仍有数据")
	}
	DeletePattern("**")
	if Len() != 0 {
		t.Errorf("'**' 后 Len=%d, want 0", Len())
	}
}

func TestKeysLen(t *testing.T) {
	resetForTest()
	if Len() != 0 || len(Keys()) != 0 {
		t.Fatal("初始应为空")
	}
	Set("a", 1, 0)
	Set("b", 2, 0)
	if Len() != 2 {
		t.Errorf("Len=%d, want 2", Len())
	}
	keys := Keys()
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("Keys=%v, want [a b]", keys)
	}
}

func TestRegisterLoader_Basic(t *testing.T) {
	resetForTest()
	called := 0
	RegisterLoader("user.*", time.Minute, func(ctx context.Context, key string) (any, error) {
		called++
		return "loaded:" + key, nil
	})
	v, err := Get(context.Background(), "user.42")
	if err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if v != "loaded:user.42" {
		t.Errorf("got %v, want loaded:user.42", v)
	}
	if called != 1 {
		t.Errorf("loader called %d 次, want 1", called)
	}
	// 第二次应命中缓存，不再调用 loader
	v, err = Get(context.Background(), "user.42")
	if err != nil || v != "loaded:user.42" || called != 1 {
		t.Errorf("二次 Get：v=%v err=%v called=%d", v, err, called)
	}
}

func TestRegisterLoader_Specificity(t *testing.T) {
	resetForTest()
	hits := map[string]int{}
	RegisterLoader("user.**", 0, func(ctx context.Context, key string) (any, error) {
		hits["**"]++
		return "double:" + key, nil
	})
	RegisterLoader("user.*", 0, func(ctx context.Context, key string) (any, error) {
		hits["*"]++
		return "single:" + key, nil
	})
	RegisterLoader("user.42", 0, func(ctx context.Context, key string) (any, error) {
		hits["lit"]++
		return "literal:" + key, nil
	})

	v, _ := Get(context.Background(), "user.42")
	if v != "literal:user.42" {
		t.Errorf("user.42 -> %v, want literal", v)
	}
	v, _ = Get(context.Background(), "user.99")
	if v != "single:user.99" {
		t.Errorf("user.99 -> %v, want single", v)
	}
	v, _ = Get(context.Background(), "user.a.b")
	if v != "double:user.a.b" {
		t.Errorf("user.a.b -> %v, want double", v)
	}
	if hits["lit"] != 1 || hits["*"] != 1 || hits["**"] != 1 {
		t.Errorf("hits=%v, want all=1", hits)
	}
}

func TestRegisterLoader_Override(t *testing.T) {
	resetForTest()
	RegisterLoader("k.*", 0, func(ctx context.Context, key string) (any, error) { return "v1", nil })
	RegisterLoader("k.*", 0, func(ctx context.Context, key string) (any, error) { return "v2", nil })
	v, _ := Get(context.Background(), "k.x")
	if v != "v2" {
		t.Errorf("override 后 = %v, want v2", v)
	}
}

func TestLoader_ErrorWrapping(t *testing.T) {
	resetForTest()
	myErr := errors.New("upstream down")
	RegisterLoader("x.*", 0, func(ctx context.Context, key string) (any, error) { return nil, myErr })
	_, err := Get(context.Background(), "x.1")
	if !errors.Is(err, ErrLoaderFailed) {
		t.Errorf("err=%v 应包含 ErrLoaderFailed", err)
	}
}

func TestLoader_Singleflight(t *testing.T) {
	resetForTest()
	var calls atomic.Int32
	RegisterLoader("slow.*", time.Minute, func(ctx context.Context, key string) (any, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return key, nil
	})

	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := Get(context.Background(), "slow.x")
			if err != nil || v != "slow.x" {
				t.Errorf("Get returned v=%v err=%v", v, err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("loader 调用 %d 次, singleflight 应去重为 1", got)
	}
}

// 等待中的调用方因自身 ctx 取消应立刻返回 ctx.Err()，而 loader 在后台跑完仍能服务后续 Get。
func TestLoader_WaiterContextCancellation(t *testing.T) {
	resetForTest()
	var calls atomic.Int32
	loaderStarted := make(chan struct{})
	loaderRelease := make(chan struct{})
	RegisterLoader("blk.*", time.Minute, func(ctx context.Context, key string) (any, error) {
		calls.Add(1)
		close(loaderStarted)
		<-loaderRelease
		return "loaded:" + key, nil
	})

	// 第一个调用方：用 background ctx，作为 singleflight 的赢家，会一直等
	winnerDone := make(chan error, 1)
	var winnerVal any
	go func() {
		v, err := Get(context.Background(), "blk.x")
		winnerVal = v
		winnerDone <- err
	}()
	<-loaderStarted

	// 第二个调用方：等待者，自己的 ctx 取消应立即返回
	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := Get(ctx, "blk.x")
		waiterDone <- err
	}()
	cancel()

	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiter err=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter 未及时返回")
	}

	// 释放 loader，winner 应正常拿到结果
	close(loaderRelease)
	if err := <-winnerDone; err != nil {
		t.Fatalf("winner err=%v", err)
	}
	if winnerVal != "loaded:blk.x" {
		t.Errorf("winner val=%v", winnerVal)
	}

	if calls.Load() != 1 {
		t.Errorf("loader 调用 %d 次, want 1", calls.Load())
	}
}

// loader 收到的 ctx 应是触发它的调用方所传入的 ctx，能携带 value。
func TestLoader_ContextPropagation(t *testing.T) {
	resetForTest()
	type ctxKey struct{}
	var got any
	RegisterLoader("ctx.*", time.Minute, func(ctx context.Context, key string) (any, error) {
		got = ctx.Value(ctxKey{})
		return "ok", nil
	})
	ctx := context.WithValue(context.Background(), ctxKey{}, "trace-id-42")
	if _, err := Get(ctx, "ctx.x"); err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if got != "trace-id-42" {
		t.Errorf("loader 收到的 ctx value = %v, want trace-id-42", got)
	}
}

func TestGet_TypedConvenience(t *testing.T) {
	resetForTest()
	Set("i", 7, 0)
	Set("s", "hello", 0)
	Set("b", true, 0)

	ctx := context.Background()
	if v, err := GetInt(ctx, "i"); err != nil || v != 7 {
		t.Errorf("GetInt = %v, %v", v, err)
	}
	if v, err := GetString(ctx, "s"); err != nil || v != "hello" {
		t.Errorf("GetString = %v, %v", v, err)
	}
	if v, err := GetBool(ctx, "b"); err != nil || !v {
		t.Errorf("GetBool = %v, %v", v, err)
	}

	// 类型不匹配
	if _, err := GetInt(ctx, "s"); !errors.Is(err, ErrTypeMismatch) {
		t.Errorf("GetInt(string key) err=%v, want ErrTypeMismatch", err)
	}
}

type myStruct struct{ N int }

func TestGetT_Struct(t *testing.T) {
	resetForTest()
	ctx := context.Background()
	Set("u", &myStruct{N: 9}, 0)
	v, err := GetT[*myStruct](ctx, "u")
	if err != nil {
		t.Fatalf("GetT err: %v", err)
	}
	if v == nil || v.N != 9 {
		t.Errorf("GetT got %v, want {N:9}", v)
	}
	// 类型不匹配
	if _, err := GetT[*myStruct](ctx, "u"); err != nil {
		t.Errorf("re-Get same type 应成功: %v", err)
	}
	if _, err := GetT[string](ctx, "u"); !errors.Is(err, ErrTypeMismatch) {
		t.Errorf("GetT[string] = %v, want ErrTypeMismatch", err)
	}
}

func TestStartStop_Idempotent(t *testing.T) {
	resetForTest()
	Start()
	Start() // 第二次应幂等
	Set("a", 1, 0)
	if !Has("a") {
		t.Error("Start 后应可正常使用")
	}
	Stop()
	if Has("a") {
		t.Error("Stop 后缓存应被清空")
	}
	Stop() // 重复调用幂等
}

func TestJanitor_CleansExpired(t *testing.T) {
	resetForTest()
	// 测试期间用极短的 tick 触发清理
	old := janitorTick
	janitorTick = 20 * time.Millisecond
	defer func() { janitorTick = old }()

	Start()
	defer Stop()

	Set("k", "v", 10*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	// janitor 应已运行至少 1 次并清掉条目
	mu.RLock()
	_, exists := items["k"]
	mu.RUnlock()
	if exists {
		t.Error("janitor 未清理过期 key")
	}
}
