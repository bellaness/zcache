package zcache

import "strings"

// splitParts 按 . 分割 key 或 pattern 为段。
func splitParts(s string) []string {
	return strings.Split(s, ".")
}

// match 判断 keyParts 是否匹配 patternParts。
//   - "*"  匹配恰好一个段
//   - "**" 匹配 0 或多个连续段
//
// 算法：双指针 + ** 回溯（与经典 glob 匹配同构）。
func match(patternParts, keyParts []string) bool {
	pi, ki := 0, 0
	starPi, starKi := -1, 0
	for ki < len(keyParts) {
		if pi < len(patternParts) {
			switch patternParts[pi] {
			case "**":
				starPi = pi
				starKi = ki
				pi++ // 先尝试匹配 0 个段
				continue
			case "*":
				pi++
				ki++
				continue
			default:
				if patternParts[pi] == keyParts[ki] {
					pi++
					ki++
					continue
				}
			}
		}
		// 不匹配；若有 ** 回溯点则消费多 1 个 key 段后重试
		if starPi >= 0 {
			pi = starPi + 1
			starKi++
			ki = starKi
			continue
		}
		return false
	}
	// key 已耗尽；pattern 末尾剩余必须全是 **（否则还需消费段）
	for pi < len(patternParts) && patternParts[pi] == "**" {
		pi++
	}
	return pi == len(patternParts)
}

// computeScore 计算 pattern 的具体度评分。元组按字典序比较，越大越具体。
//   - 字面段越多越具体
//   - 同字面段数下，* 越多越具体（* 至少约束 1 段，比 ** 严格）
//   - 再平局时，** 越少越具体
func computeScore(parts []string) [3]int {
	var lit, single, dbl int
	for _, p := range parts {
		switch p {
		case "**":
			dbl++
		case "*":
			single++
		default:
			lit++
		}
	}
	return [3]int{lit, single, -dbl}
}

// DeletePattern 按通配符模式批量删除，返回删除条数。
//
//	"*"  匹配只有一个段的 key
//	"**" 删除全部
//	"a.*.z"  匹配 a.X.z（恰好 3 段）
//	"a.**.z" 匹配 a.z, a.X.z, a.X.Y.z 等（** 含 0 段）
func DeletePattern(pattern string) int {
	parts := splitParts(pattern)
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for k := range items {
		if match(parts, splitParts(k)) {
			delete(items, k)
			n++
		}
	}
	return n
}
