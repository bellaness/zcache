package zcache

// Start 启动缓存：开启后台 janitor 协程定期清理过期条目。
// 由 appinit 在服务启动阶段调用，可重复调用（幂等）。
func Start() {
	startJanitor()
}

// Stop 停止缓存：关闭 janitor 协程并清空全部缓存条目。
// 注意：已注册的 loader 不会被清空；如需重置请调用 RegisterLoader 覆盖。
func Stop() {
	stopJanitor()
	clearAll()
}
