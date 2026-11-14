package telemetry

// switch_free_gate_test.go — 遥测「零环境变量」门禁（2026-11-14 重写）。
//
// ============ 历史与升级理由 ============
//
// 2026-11-07 删除了三个「是否启用」开关（WRITE_ON_CHANGE / DUAL_WRITE /
// ROLLUP_WRITE），当时保留了 DUAL_WRITE_QUEUE 与 HEARTBEAT_SECONDS 作为「参数」，
// 并留了一条「三个开关名零命中」的门禁。
//
// 2026-11-14 的结论是：**参数与开关的边界在实践中守不住**。事故链正是
// ROLLUP_READ（默认 false）+ FRAME_DECODE_MAX_WINDOW（默认 1h）两个变量的生产值
// 靠 compose 覆盖，重部署丢变量 ⇒ 24h/7d 全灭。于是本轮把遥测的**全部**环境变量
// 删除（含两个「参数」），改为代码常量 / DualWriterConfig 显式注入。
//
// 本门禁也从「三个开关名零命中」升级为「EHOME_TELEMETRY_ 前缀零命中」：
// 只要有人再给遥测加 env，名字必然带这个前缀，门禁立刻红。
//
// ============ 只扫带引号的字面量 ============
//
// 注释里解释历史（例如「过去叫 EHOME_TELEMETRY_ROLLUP_READ」）不算违规；
// _test.go 与 _sim.go 里显式出现旧变量名是**故意的**（断言它已失效）。
// 因此匹配的是 `"EHOME_TELEMETRY_XXX"` 这样的字面量，且跳过测试/仿真文件。

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// telemetryEnvPrefix 用拼接构造：否则本文件自己会被门禁扫到。
const telemetryEnvPrefix = "EHOME_" + "TELEMETRY_"

func TestTelemetryHasNoEnvSurface(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve backend root: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		t.Fatalf("backend root %s 不像 backend 目录（没有 go.mod）: %v", root, statErr)
	}

	pattern := regexp.MustCompile(`"` + telemetryEnvPrefix + `[A-Z0-9_]+"`)
	var hits []string

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_sim.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		for lineNo, line := range strings.Split(string(data), string(byte(10))) {
			if pattern.MatchString(line) {
				hits = append(hits, rel+":"+itoaSmall(lineNo+1)+"  "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk backend tree: %v", walkErr)
	}

	if len(hits) > 0 {
		t.Errorf("遥测不读任何环境变量（2026-11-14 裁决），但源码里仍有 %d 处命中：", len(hits))
		for _, h := range hits {
			t.Errorf("  %s", h)
		}
	}
}

// itoaSmall 是测试内的最小整数格式化（避免为一行日志引入 strconv）。
func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
