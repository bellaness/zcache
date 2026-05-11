package zcache

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		key     string
		want    bool
	}{
		// 字面匹配
		{"a", "a", true},
		{"a", "b", false},
		{"a.b.c", "a.b.c", true},
		{"a.b.c", "a.b.d", false},
		{"a.b", "a.b.c", false},

		// 单段通配 *
		{"*", "a", true},
		{"*", "a.b", false},
		{"a.*", "a.b", true},
		{"a.*", "a", false},
		{"a.*", "a.b.c", false},
		{"a.*.z", "a.b.z", true},
		{"a.*.z", "a.b.c.z", false},

		// 多段通配 **
		{"**", "a", true},
		{"**", "a.b", true},
		{"**", "a.b.c.d", true},
		{"a.**", "a", true}, // ** 含 0 段
		{"a.**", "a.b", true},
		{"a.**", "a.b.c", true},
		{"**.z", "z", true}, // ** 含 0 段
		{"**.z", "a.z", true},
		{"**.z", "a.b.z", true},
		{"**.z", "a.b.x", false},
		{"a.**.z", "a.z", true}, // 关键边界：含 0 段
		{"a.**.z", "a.b.z", true},
		{"a.**.z", "a.b.c.z", true},
		{"a.**.z", "x.z", false},
		{"a.**.z", "a.b", false},

		// 混合
		{"a.*.b.**", "a.x.b", true},
		{"a.*.b.**", "a.x.b.y", true},
		{"a.*.b.**", "a.x.b.y.z", true},
		{"a.*.b.**", "a.x.y.b", false},

		// 多个 ** 折叠
		{"**.**", "a", true},
		{"**.**", "a.b.c", true},
		{"a.**.**.b", "a.b", true},
		{"a.**.**.b", "a.x.b", true},
		{"a.**.**.b", "a.x.y.b", true},
	}
	for _, tc := range cases {
		got := match(splitParts(tc.pattern), splitParts(tc.key))
		if got != tc.want {
			t.Errorf("match(%q, %q) = %v, want %v", tc.pattern, tc.key, got, tc.want)
		}
	}
}

func TestComputeScore(t *testing.T) {
	cases := []struct {
		pattern string
		want    [3]int
	}{
		{"user.42", [3]int{2, 0, 0}},
		{"user.*", [3]int{1, 1, 0}},
		{"user.**", [3]int{1, 0, -1}},
		{"a.*.c", [3]int{2, 1, 0}},
		{"a.**.c", [3]int{2, 0, -1}},
		{"**", [3]int{0, 0, -1}},
	}
	for _, tc := range cases {
		got := computeScore(splitParts(tc.pattern))
		if got != tc.want {
			t.Errorf("computeScore(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

// 验证：字面段 > * > **，且评分顺序与"具体度"直觉一致
func TestSpecificityOrdering(t *testing.T) {
	// user.42 应严格优于 user.* 应严格优于 user.**
	tight := computeScore(splitParts("user.42"))
	mid := computeScore(splitParts("user.*"))
	loose := computeScore(splitParts("user.**"))
	if !tupleGreater(tight, mid) {
		t.Errorf("expected user.42 (%v) > user.* (%v)", tight, mid)
	}
	if !tupleGreater(mid, loose) {
		t.Errorf("expected user.* (%v) > user.** (%v)", mid, loose)
	}
}

func tupleGreater(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
