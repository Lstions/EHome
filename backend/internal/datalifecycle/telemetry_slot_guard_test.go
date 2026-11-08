package datalifecycle

// telemetry_slot_guard_test.go — slot 字典**必须长期保留**的守卫（task-24 前提 ②）。
//
// ============ 为什么这是一个「机制 vs 纪律」的问题 ============
//
// 帧表里存的是量化后的位流（1B flags + changed_mask + 位打包整数码）。要把它还原成
// (字段名, 浮点值)，必须查 telemetry_slot：它记录「这个 slot_id 的字段集合 + 每个字段
// 的 Min/Step/Bits 量化规格快照」。
//
// **字典一旦被清掉，帧表就整体变成不可解位流** —— 而 task-24 的整个可逆性论证
// （「rollup 可从帧表重建」）都建立在这上面。
//
// 现状是：生产代码里**没有任何删除 telemetry_slot 的路径**（只有 _test.go 的清理）。
// 但那是**纪律**（"目前没人写删除代码"），不是**机制**（"结构上不可能被删"）。
//
// 本用例把纪律变成机制：如果将来有人给 telemetry_slot 加了保留期清理 / purge / 分区退役，
// 这里会红，并把人引到本注释。
//
// ============ 如果你是因为这条红了才看到这里 ============
//
// 先问自己：**清除 slot 之后，历史帧还能解吗？** 如果不能，那这条清理就是错的 ——
// 帧表的价值全部依赖字典。若确实必须清理，正确做法是「清理时同时把对应时段的帧
// 转成可独立理解的形态（或导出）」，而不是直接删字典行。
//
// 如果确实是安全的（例如按 slot 的 last_used 退役且已确认无任何帧引用），
// 那么把下面的 allowlist 更新，并在提交信息里说明「已确认无帧引用」的证据。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// slotRetirementAllowlist 是**允许**出现 telemetry_slot 删除语句的文件白名单。
//
// 目前为空：一张都不许删。新增条目需要在本文件注释里写清「为什么安全」。
var slotRetirementAllowlist = map[string]string{}

// TestTelemetrySlotHasNoRetirementPath 扫描生产代码，断言没有删除 telemetry_slot 的路径。
//
// 扫描方式：遍历仓库内**非测试**的 .go 文件，找同一行里同时出现 telemetry_slot
// 与 delete/drop/purge/truncate 的语句。这是启发式检查 —— 它抓的是真实会发生的
// 那种改动（有人给生命周期代码加一条 DROP/DELETE），不是对抗性绕过。
func TestTelemetrySlotHasNoRetirementPath(t *testing.T) {
	// 只扫 backend/：Go 源码全在这里。扫整个仓库会遍历 .agents/worktrees 与
	// frontend/node_modules —— 实测把这条用例从 <1s 拖到 57s，CI 上不可接受。
	root := filepath.Join(repoRootForGuard(t), "backend")
	bad := make([]string, 0)

	verbs := []string{"delete", "drop", "purge", "truncate", "detach"}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 读不到就跳过：守卫不该因为权限问题误报
		}
		if d.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == "node_modules" || base == ".tmp-go" || strings.HasPrefix(base, ".tmp-") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			low := strings.ToLower(line)
			if !strings.Contains(low, "telemetry_slot") {
				continue
			}
			for _, v := range verbs {
				if strings.Contains(low, v) {
					rel, _ := filepath.Rel(root, path)
					entry := rel + ":" + itoa(i+1)
					if _, ok := slotRetirementAllowlist[rel]; !ok {
						bad = append(bad, entry+"  ->  "+strings.TrimSpace(line))
					}
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if len(bad) > 0 {
		t.Fatalf("发现 telemetry_slot 的删除/退役路径（会不可逆地毁掉全部历史帧的可解性）：\n  %s\n\n"+
			"帧的字段名与量化规格**只存在于** slot 字典里；字典没了，帧就是不可解位流。\n"+
			"task-24「rollup 可从帧表重建」的可逆性论证完全依赖这一点。\n"+
			"若确需退役，请先证明「该 slot 已无任何帧引用」，再更新 slotRetirementAllowlist 并写明证据。",
			strings.Join(bad, "\n  "))
	}
}

// repoRootForGuard 从当前测试文件位置回溯到仓库根（含 go.mod 的 backend 的上一级）。
func repoRootForGuard(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// wd = .../backend/internal/datalifecycle
	p := wd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(p, "backend", "go.mod")); err == nil {
			return p
		}
		p = filepath.Dir(p)
	}
	t.Fatalf("找不到含 backend/go.mod 的仓库根，wd=%s", wd)
	return ""
}

// itoa 是 strconv.Itoa 的极小替身（避免为了一个转换引入 import）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
