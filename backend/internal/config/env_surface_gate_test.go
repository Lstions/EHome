package config

// env_surface_gate_test.go — 生产环境变量面的**白名单门禁**（2026-11-14）。
//
// ============ 为什么需要这条门禁 ============
//
// 2026-11-14 长窗图表全坏，根因不是数据、不是代码逻辑，而是「行为被放进了环境变量」：
// 生产靠 EHOME_TELEMETRY_ROLLUP_READ=true 与
// EHOME_TELEMETRY_FRAME_DECODE_MAX_WINDOW=6h 覆盖代码默认值（false / 1h）。
// 重部署时 compose 被仓库模板覆盖，两个变量丢失 ⇒ 24h/7d 立刻全灭，
// 而启动日志一个字都没提 —— 排查从「数据是不是坏了」开始，花了很久才落到配置。
//
// 裁决：**环境变量只回答「连哪里、用什么身份」**（数据库、JWT、证书、监听地址、
// 静态资源、保留期）。凡是回答「代码该怎么跑」的，一律写死为代码常量。
//
// 本门禁把这条裁决变成可执行断言：backend 的 internal/ 与 cmd/ 下（不含 _test.go
// 与 _sim.go）出现的每一个 "EHOME_XXX" 字面量，都必须在白名单里。
//
// ============ 添加新环境变量的正确姿势 ============
//
// 先问一句：「它是不是部署身份？」不是 ⇒ 不要加，改成常量或 config.yaml 默认值。
// 如果确实是身份类（例如换一台机器必须不同），加进 allowedEnvVars 并在 PR 里
// 说明为什么不能写死。

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// allowedEnvVars 是**唯一**允许出现在生产代码里的 EHOME_* 变量。
//
// 不在表里的两种典型：
//   - 行为开关（DEVICE_ENABLED / ROLLUP_READ / RAW_DIAGNOSTICS_ENABLED …）
//   - 调参旋钮（HEARTBEAT_SECONDS / PARSER_SHARDS / FRAME_DECODE_MAX_WINDOW …）
var allowedEnvVars = map[string]bool{
	// HTTP / 进程
	"EHOME_SERVER_ADDR": true,
	"EHOME_ENV":         true,
	// 数据库
	"EHOME_DB_HOST":     true,
	"EHOME_DB_PORT":     true,
	"EHOME_DB_USER":     true,
	"EHOME_DB_PASSWORD": true,
	"EHOME_DB_NAME":     true,
	"EHOME_DB_SSLMODE":  true,
	// 安全 / 对外身份
	"EHOME_JWT_SECRET":      true,
	"EHOME_ALLOWED_ORIGINS": true,
	"EHOME_STATIC_DIR":      true,
	"EHOME_EXTERNAL_HOST":   true,
	// 设备 mTLS 连接材料（是否监听由材料齐全度推导，没有 enabled 开关）
	"EHOME_DEVICE_ADDR":           true,
	"EHOME_DEVICE_CERT_FILE":      true,
	"EHOME_DEVICE_KEY_FILE":       true,
	"EHOME_DEVICE_CLIENT_CA_FILE": true,
	// 初始管理员种子
	"EHOME_ADMIN_USERNAME": true,
	"EHOME_ADMIN_PASSWORD": true,
	"EHOME_ADMIN_EMAIL":    true,
	// 产品策略（保留期）
	"EHOME_DATA_RETENTION_DAYS": true,
	// ehomectl 一次性 CLI 入参（不是服务运行期配置）
	"EHOME_NEW_ADMIN_PASSWORD": true,
}

// envLiteralPattern 只匹配**带引号的字面量**：注释里提到旧变量名（解释历史）不算违规。
func TestProductionEnvSurfaceIsWhitelisted(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve backend root: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		t.Fatalf("backend root %s 不像 backend 目录（没有 go.mod）: %v", root, statErr)
	}

	pattern := regexp.MustCompile(`"EHOME_[A-Z0-9_]+"`)
	var violations []string

	for _, sub := range []string{"internal", "cmd"} {
		walkErr := filepath.WalkDir(filepath.Join(root, sub), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			// 测试与仿真构建文件允许提及旧变量名（它们要断言「已失效」）。
			if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_sim.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			rel, _ := filepath.Rel(root, path)
			for lineNo, line := range strings.Split(string(data), string(byte(10))) {
				for _, lit := range pattern.FindAllString(line, -1) {
					name := strings.Trim(lit, `"`)
					if !allowedEnvVars[name] {
						violations = append(violations, rel+":"+itoa(lineNo+1)+"  "+name)
					}
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", sub, walkErr)
		}
	}

	if len(violations) > 0 {
		t.Errorf("发现 %d 处未列入白名单的环境变量 —— 环境变量只允许表达「部署身份」：", len(violations))
		for _, v := range violations {
			t.Errorf("  %s", v)
		}
		t.Log("行为/调参请写死为代码常量；确属部署身份再登记到 allowedEnvVars 并说明理由。")
	}
}

// itoa 避免引入 strconv 只为拼一行字符串（本文件刻意保持最小依赖）。
func itoa(n int) string {
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
