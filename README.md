# zcache —— 轻量级进程内 Go 对象缓存

`zcache` 是一个**单进程、零序列化、按需加载**的 Go 内存缓存。直接存储 `any`
对象，支持 TTL，按 key 模式注册 loader 实现"未命中自动加载"，并提供
层级通配符的批量删除。

- **不**跨节点：单进程可见
- **不**做序列化：直接持有 Go 对象
- 线程安全：多 goroutine 并发存取
- 零外部依赖：仅依赖标准库 + `golang.org/x/sync/singleflight`

---

## 安装

```bash
go get github.com/bellaness/zcache
```

> 将 `bellaness` 替换为实际仓库路径。下文示例统一以 `zcache` 作为包名引用。

---

## 快速上手

```go
package main

import (
    "context"
    "fmt"
    "strings"
    "time"

    "github.com/bellaness/zcache"
)

type User struct{ Name string }

func main() {
    zcache.Start()
    defer zcache.Stop()

    // 注册一次：所有 user.* 的 key 都会走这个 loader
    zcache.RegisterLoader("user.*", 5*time.Minute, func(ctx context.Context, key string) (any, error) {
        id := strings.TrimPrefix(key, "user.")
        return &User{Name: "user-" + id}, nil
    })

    ctx := context.Background()
    u, _ := zcache.GetT[*User](ctx, "user.42") // 第一次：触发 loader
    fmt.Println(u.Name)
    u, _ = zcache.GetT[*User](ctx, "user.42")  // 第二次：直接命中
    fmt.Println(u.Name)
}
```

---

## 1. 生命周期

| 函数 | 说明 |
|---|---|
| `Start()` | 启动后台 janitor 协程，定期扫描清理过期条目（默认每分钟一次）。幂等。 |
| `Stop()`  | 同步停止 janitor 并清空全部缓存条目。已注册的 loader **不会**被清空。 |

`Start()` 应在程序启动早期调用一次；`Stop()` 在优雅关闭阶段调用。
未调用 `Start()` 也能正常 `Set`/`Get`，只是不会有后台清理（过期条目仍会在
下一次 `Get` / `Has` / `Keys` / `Len` 时被识别为过期）。

---

## 2. Key 命名规范

key 用 `.` 分成"段"（part）。例如：

```
user.42
order.7.summary
config.feature.payment.enabled
```

这一约定贯穿所有 API：`DeletePattern` 与 `RegisterLoader` 都以"段"为最小匹配单元。

---

## 3. 通配符语法

通配符既用于 `DeletePattern`，也用于 `RegisterLoader` 的 pattern 参数。

| 符号 | 含义 |
|---|---|
| `*`  | 匹配恰好**一段** |
| `**` | 匹配**零段或多段**（连续） |

### 匹配示例

| pattern    | 命中 key                    | 不命中                   |
|---|---|---|
| `*`        | `a`, `xyz`                  | `a.b`                    |
| `a.*`      | `a.b`, `a.x`                | `a`, `a.b.c`             |
| `a.*.z`    | `a.b.z`, `a.x.z`            | `a.z`, `a.b.c.z`         |
| `a.**`     | `a`, `a.b`, `a.b.c.d`       | `x.a`                    |
| `a.**.z`   | `a.z`, `a.b.z`, `a.b.c.z`   | `a.b`, `x.z`             |
| `**`       | 任意 key                    | —                        |

> 关键边界：`a.**.z` **会**匹配 `a.z`（`**` 含零段，遵循文件路径与 MQTT 主题惯例）。

---

## 4. 写入：Set

```go
func Set(key string, value any, ttl time.Duration)
```

- `ttl > 0`：到期后自动清理
- `ttl <= 0`：永不过期

```go
zcache.Set("user.42", &User{Name: "Eric"}, 5*time.Minute)
zcache.Set("config.version", "v1", 0)  // 永不过期
```

---

## 5. 读取：Get 系列

```go
func Get(ctx context.Context, key string) (any, error)
func GetT[T any](ctx context.Context, key string) (T, error)
func GetInt(ctx context.Context, key string) (int, error)
func GetString(ctx context.Context, key string) (string, error)
func GetBool(ctx context.Context, key string) (bool, error)
```

读取行为：

1. 命中且未过期 → 直接返回（`ctx` 不会被使用）
2. 未命中（或已过期被清理）+ 匹配到已注册 loader → 调用 loader（`ctx` 一路传入），自动 `Set`、返回
3. 未命中且无 loader → 返回 `ErrNotFound`

```go
v, err := zcache.GetT[*User](ctx, "user.42")
if errors.Is(err, zcache.ErrNotFound) {
    // 缓存中没有，且未注册 loader
}
if errors.Is(err, zcache.ErrTypeMismatch) {
    // 缓存里这个 key 存的不是 *User
}
if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
    // 等待 loader 期间 ctx 被取消/超时
}
```

便捷的 `GetInt` / `GetString` / `GetBool` 是 `GetT[int]` / `GetT[string]` / `GetT[bool]` 的薄封装。

### `ctx` 的语义细节

- **命中**直接返回，**不**检查 `ctx` 是否已取消（命中是即时操作，无意义浪费一次系统调用）
- 等待 loader 期间，`ctx` 取消会让当前调用立即返回 `ctx.Err()`
- 由 singleflight 决定首次进入 loader 的"赢家"调用方，loader 收到的就是赢家的 `ctx`；
  其他等待者用各自的 `ctx` 等结果，互不干扰
- 等待者取消并不会终止 loader——loader 在后台跑完，结果对其他未取消的等待者依然有效

---

## 6. Loader：未命中自动加载

类似 `groupcache` 的"按需加载"模式，但 loader **不在 Get 调用处传入**——
而是在初始化阶段按 key 模式注册一次，之后所有 `Get` 系列函数都会自动派发。

```go
func RegisterLoader(pattern string, ttl time.Duration, loader Loader)

type Loader func(ctx context.Context, key string) (value any, err error)
```

- `pattern`：使用第 3 节的通配符语法
- `ttl`：loader 加载到的值的 TTL；`<=0` 表示永不过期
- `ctx`：来自触发加载的 `Get` 调用方；可向下游 db/HTTP 透传，做超时控制与 trace 注入
- 同一 `pattern` 重复注册会**覆盖**前一个

### 推荐用法：在程序启动初期集中注册

```go
// 例如在某个 init() 或启动钩子里
zcache.RegisterLoader("user.*", 5*time.Minute, func(ctx context.Context, key string) (any, error) {
    id := strings.TrimPrefix(key, "user.")
    return userrepo.LoadByID(ctx, id)
})

zcache.RegisterLoader("order.*.summary", time.Minute, func(ctx context.Context, key string) (any, error) {
    parts := strings.Split(key, ".")
    return orderrepo.Summary(ctx, parts[1])
})

zcache.RegisterLoader("config.**", 0, func(ctx context.Context, key string) (any, error) {
    return configstore.Lookup(ctx, key)
})
```

业务代码无需关心 loader 是否存在：

```go
u, err := zcache.GetT[*User](ctx, "user.42")          // 自动走 user.* loader
sum, _ := zcache.GetString(ctx, "order.7.summary")    // 自动走 order.*.summary loader
```

### 多 loader 同时匹配时的优先级

按"**最具体优先**"选取，规则按字典序比较三元组 `(literal_count, single_count, -double_count)`：

1. 字面段越多越具体
2. 同字面段数下，`*` 多者更具体（`*` 比 `**` 严格）
3. 仍平局，`**` 少者更具体
4. 仍平局，按**注册顺序**先者胜出

示例：

| 已注册 pattern                                  | `Get("user.42")`  | `Get("user.99")`  | `Get("user.a.b")`  |
|---|---|---|---|
| `user.42`、`user.*`、`user.**` 同时存在 | `user.42` 胜      | `user.*` 胜       | `user.**` 胜       |

### Loader 错误处理

loader 返回的错误会被包装成 `ErrLoaderFailed`：

```go
if errors.Is(err, zcache.ErrLoaderFailed) {
    // 上游加载失败；用 errors.Unwrap 取原始错误
}
```

### 防雪崩（singleflight）

当多个 goroutine 同时请求同一未命中 key 时，loader 只会被调用 **1 次**，
其他 goroutine 会复用首次结果。这是 `groupcache` 风格的核心保证。

> 等待者持有自己的 `ctx`，可独立取消而不影响 loader；loader 仍以首个调用方的 `ctx`
> 在后台跑完，结果对未取消的等待者依然有效。

---

## 7. 删除

### 单 key / 多 key

```go
func Delete(keys ...string)
```

```go
zcache.Delete("user.42")
zcache.Delete("user.42", "user.99", "session.abc")
```

### 模式删除

```go
func DeletePattern(pattern string) int  // 返回删除条数
```

```go
zcache.DeletePattern("user.42")          // 等价于 Delete("user.42")
zcache.DeletePattern("user.*")           // 删 user.X（恰好两段）
zcache.DeletePattern("session.**")       // 删所有 session 开头的
zcache.DeletePattern("**")               // 清空所有缓存
zcache.DeletePattern("*")                // 删所有只有一段的 key
```

> `DeletePattern` 实现为全表扫描（O(N)）。对于"轻量级"的目标负载（数千～数万条目）足够；
> 切勿在每次请求路径上反复调用大范围的 `DeletePattern`。

---

## 8. 查询辅助

```go
func Has(key string) bool   // 仅检查存在，不触发 loader
func Keys() []string        // 当前所有未过期 key 的快照
func Len() int              // 当前未过期条目总数
```

`Has` 与 `Get` 的差别：`Has` 不会触发 loader，也不会因未命中而做任何写入。
适合做"先检查再写入"这种不希望意外加载的场景。

---

## 9. 错误

```go
var (
    ErrNotFound     = errors.New("zcache: key 不存在")
    ErrTypeMismatch = errors.New("zcache: 缓存值类型不匹配")
    ErrLoaderFailed = errors.New("zcache: loader 执行失败")
)
```

全部都用 `errors.Is` 判断；`ErrLoaderFailed` 包装了 loader 自身的原始错误，
可用 `errors.Unwrap` 取出。

---

## 10. 完整示例：HTTP handler 集成

```go
package userhandler

import (
    "context"
    "errors"
    "log"
    "net/http"
    "strings"
    "time"

    "github.com/bellaness/zcache"
)

// 在某个初始化函数里注册一次
func init() {
    zcache.RegisterLoader("user.*", 5*time.Minute, func(ctx context.Context, key string) (any, error) {
        id := strings.TrimPrefix(key, "user.")
        u, err := db.LoadUser(ctx, id)
        if err != nil {
            return nil, err
        }
        return u, nil
    })
}

func handleGetUser(w http.ResponseWriter, r *http.Request) {
    id := r.URL.Query().Get("id")
    u, err := zcache.GetT[*User](r.Context(), "user."+id)
    switch {
    case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
        return // 客户端断连/超时，无需响应
    case errors.Is(err, zcache.ErrLoaderFailed):
        http.Error(w, "上游错误", http.StatusBadGateway)
        return
    case errors.Is(err, zcache.ErrTypeMismatch):
        log.Printf("缓存值类型错乱: %v", err)
        zcache.Delete("user." + id) // 自愈
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    case err != nil:
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    writeJSON(w, u)
}

// 用户更新后，主动失效
func handleUpdateUser(w http.ResponseWriter, r *http.Request) {
    // ... 写库 ...
    zcache.Delete("user." + id)
}
```

---

## 11. 设计要点速记

- **包级 API + 包级单例**：无需 `New()` 工厂；一个进程一份缓存
- **Loader 注册时绑定 TTL**：调用方写得最省，且方便统一治理
- **GetT 是顶级范型函数**：Go 不支持方法级范型，因此 `GetT[T]` 不能挂在某个 `Cache` 类型上
- **`ctx` 一路下沉到 loader**：HTTP 请求 ctx、超时、tracing 都能透传；等待者还能独立取消
- **不持久化、不淘汰**：内存够大、TTL 够准，是 zcache 的工作前提；如需 LRU/容量限制请另选库

---

## 12. 何时**不要**用 zcache

- 需要跨节点共享缓存 → 用 Redis / Memcached
- 需要容量上限 / LRU / LFU 淘汰策略 → 用 [`ristretto`](https://github.com/dgraph-io/ristretto)、[`otter`](https://github.com/maypok86/otter) 等
- 需要持久化、跨进程恢复 → 用嵌入式 KV（BoltDB、Badger）
- 缓存值非常大且数量很多，担心 GC 压力 → 用 [`bigcache`](https://github.com/allegro/bigcache)、[`fastcache`](https://github.com/VictoriaMetrics/fastcache) 等堆外方案
