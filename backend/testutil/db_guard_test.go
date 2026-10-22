package testutil

import (
	"testing"
)

// TestIsProductionDBName 钉住生产库名守卫的匹配口径。
//
// 背景（2026-10-22 事故）：PG 集成测试带 EHOME_DB_NAME=ehome 跑，绕过 schema 隔离
// 直接写进生产库 public schema，留下 2 条同键双 active 的孤儿行 —— 而这类双 active
// 会让下一次部署的迁移自检 fail-closed 拒绝启动。
func TestIsProductionDBName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"ehome", true},
		{"EHOME", true},   // 大小写不敏感（PG 库名本来不区分）
		{" ehome ", true}, // 前后空白（env 里手滑带空格）
		{"ehome_test", false},
		{"ehome_e2e", false},
		{"ehome_merge_prod", false},
		{"ehome_sg_final", false},
		{"", false},
		{"postgres", false},
	}
	for _, c := range cases {
		if got := IsProductionDBName(c.name); got != c.want {
			t.Errorf("IsProductionDBName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
