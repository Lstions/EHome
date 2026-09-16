package models

// 模型层「默认值陷阱」门禁（回归锁）。
//
// 背景：本仓已**五次**踩到同一个 GORM 语义陷阱 ——
// 带非零 `gorm:"default:X"` 的列，其**显式零值**在 INSERT 时会被静默替换成 X：
//   ① `AutomationRule.Enabled` 的 `default:true` ⇒ 禁用规则被存成启用（2026-08-23 探针实锤）
//   ② `AlertRule.Enabled` 同缺陷 ⇒ 用户创建「暂不启用」的告警规则实际立刻生效（2026-09-16 修复）
//   ③ `AutomationRule.CooldownSec` 的 `default:300` ⇒ `cooldown_sec: 0`（不冷却）无法表达（已修）
//   ④ `Node.LogStreamLevel` 的 `default:2` ⇒ 实测 `Create(level:0)` 回读为 2（INFO）
//   ⑤ 5 处 `Enabled bool default:true`（Channel/EdgeDevice/GPIOConfig/PWMConfig/User）仍在，
//      调用方靠**手工补偿**规避（见 `handler_periph.go` 的 `desiredEnabled` 兜底）
//
// 本门禁的作用：把「哪些字段带非零 default」**固定下来并要求逐条登记**，
// 使新增此类 tag 时必须回答「显式零值对它有语义吗？」，而不是让它默默溜进模型。
//
// ── 本门禁查不了什么（诚实声明）──────────────────────────────────────────
//   · 查不了**该不该**带 default（那是产品语义）；它只强制「带了的必须登记」；
//   · 查不了零值在**运行时**是否真的被覆盖（那要探针/行为测试）；
//   · 只覆盖 `internal/models/`，不覆盖别处的裸 SQL 建表；
//   · 登记理由是**人工判断**，门禁只能保证「有人回答过」，不能保证「答得对」。
//
// ── 变红条件 ──────────────────────────────────────────────────────────────
//   · 新增/删除任何「非零 default」tag 而未同步本登记表（**两个方向都会红**，防登记表腐烂）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// nonZeroDefaultPattern 匹配 gorm tag 里的 `default:<非零>`。
// 非零 = 不是 0 / '0' / false / ”（这些是零值默认，不会丢数据）。
var nonZeroDefaultPattern = regexp.MustCompile("gorm:\"[^\"]*default:([^;\"]+)")

// registeredNonZeroDefaults 是**已登记**的非零 default 字段全集。
// key = 文件名:结构体名.字段名，value = 为什么这个 default 不会（或暂时不会）造成数据丢失。
// 新增此类 tag 必须在此登记。
var registeredNonZeroDefaults = map[string]string{"alert.go:AlertRule.Comparator": "非数值枚举，API 强制必填；零值 空串 非法",
	"alert.go:AlertRule.Level":                                 "非数值枚举，API 强制必填；零值 空串 非法",
	"alert.go:AlertRule.SilenceSec":                            "0 与 300 都合法；handler 用 defaultAlertSilenceSec() 应用层归一，不靠 DB 回填",
	"alert.go:AlertRule.TargetType":                            "非数值枚举，API 强制必填；零值 空串 非法",
	"auth_state.go:AuthState.SecurityVersion":                  "单调计数器，0 非法，必须从 1 起",
	"automation.go:AutomationEvent.TriggerSource":              "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"backfill_job.go:BackfillJob.Status":                       "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"command_execution.go:CommandExecution.VerifiedResultJSON": "JSON 列默认 空数组，零值 空串 不是合法 JSON",
	"merge_job.go:MergeJob.Status":                             "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"merge_job.go:MergeJob.WatermarkPhase":                     "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:Channel.BusType":                                "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:Channel.Enabled":                                "bool default:true —— 显式 false 会被回填成 true；调用方须自行补偿（见 handler_periph.go 的 desiredEnabled 兜底）",
	"models.go:Channel.IntervalMs":                             "采集间隔默认 5000ms；0 非法（会导致忙轮询）",
	"models.go:DataSource.MaxFailCount":                        "故障转移阈值默认 3；0 非法",
	"models.go:DataSource.SourceType":                          "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:DataSource.Status":                              "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:DeviceConfig.Connection":                        "JSON 列默认 空对象，零值 空串 不是合法 JSON",
	"models.go:DeviceConfig.InitFlow":                          "JSON 列默认 空数组，零值 空串 不是合法 JSON",
	"models.go:DeviceConfig.Operations":                        "JSON 列默认 空对象，零值 空串 不是合法 JSON",
	"models.go:DeviceConfig.Parser":                            "JSON 列默认 空对象，零值 空串 不是合法 JSON",
	"models.go:DeviceConfig.Status":                            "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:EdgeDevice.CommandIntervals":                    "JSON 列默认 空对象，零值 空串 不是合法 JSON",
	"models.go:EdgeDevice.Enabled":                             "bool default:true —— 显式 false 会被回填成 true；调用方须自行补偿（见 handler_periph.go 的 desiredEnabled 兜底）",
	"models.go:EdgeDevice.InitState":                           "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:EdgeDevice.IntervalMs":                          "采集间隔默认 5000ms；0 非法（会导致忙轮询）",
	"models.go:EdgeDevice.Status":                              "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:GPIOConfig.Enabled":                             "bool default:true —— 显式 false 会被回填成 true；调用方须自行补偿（见 handler_periph.go 的 desiredEnabled 兜底）",
	"models.go:LogicalDevice.RetentionDays":                    "保留天数默认 90；0 语义为不保留，需显式裁决，当前无该用法",
	"models.go:Node.Capabilities":                              "JSON 列默认 空对象，零值 空串 不是合法 JSON",
	"models.go:Node.CommandEngineCapabilities":                 "JSON 列默认 空对象，零值 空串 不是合法 JSON",
	"models.go:Node.Config":                                    "JSON 列默认 空对象，零值 空串 不是合法 JSON",
	"models.go:Node.ConfigStatus":                              "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:Node.ConfigSyncState":                           "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:Node.ConnectionQuality":                         "0-100 质量分，0 表示最差但非缺省；缺省应为 100",
	"models.go:Node.DmaChannels":                               "JSON 列默认 空数组，零值 空串 不是合法 JSON",
	"models.go:Node.HardwareInfo":                              "JSON 列默认 空对象，零值 空串 不是合法 JSON",
	"models.go:Node.LogStreamLevel":                            "已知潜伏陷阱：0=ERROR 合法但 Create 会回填成 2；当前唯一写入点用 Updates(map) 不受影响",
	"models.go:Node.ProtocolVersion":                           "版本串默认 2.2，零值 空串 非法",
	"models.go:Node.Status":                                    "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:OTATask.Status":                                 "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:PWMConfig.Enabled":                              "bool default:true —— 显式 false 会被回填成 true；调用方须自行补偿（见 handler_periph.go 的 desiredEnabled 兜底）",
	"models.go:PWMConfig.Resolution":                           "PWM 分辨率默认 14 位（4-20）",
	"models.go:User.Enabled":                                   "bool default:true —— 显式 false 会被回填成 true；调用方须自行补偿（见 handler_periph.go 的 desiredEnabled 兜底）",
	"models.go:User.Role":                                      "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"models.go:User.SessionVersion":                            "单调计数器，0 非法，必须从 1 起",
	"notification_channel.go:NotificationChannel.MinLevel":     "非数值枚举，应用层显式赋值；零值 空串 不是合法取值",
	"notification_channel.go:NotificationDelivery.AttemptNo":   "重试序号，从 1 起，0 非法",
	"security_audit_event.go:SecurityAuditEvent.EventVersion":  "单调计数器，0 非法，必须从 1 起"}

// collectNonZeroDefaults 扫描 models 目录，返回「文件:结构体.字段名 → default 字面量」。
//
// 键里**必须带结构体名**：本文件初版只按「文件:字段名」做键，结果 `models.go` 里
// 5 个不同结构体的 `Enabled` 被折叠成同一个键，登记表永远对不上账 ——
// 这是门禁**自己**的分母缺陷，被首轮运行当场暴露。
func collectNonZeroDefaults(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read models dir: %v", err)
	}
	out := map[string]string{}
	structPat := regexp.MustCompile("^type\\s+(\\w+)\\s+struct\\s*\\{")
	backtick := string(rune(96))
	fieldPat := regexp.MustCompile("^\\s*([A-Z][A-Za-z0-9_]*)\\s+[A-Za-z0-9_.\\[\\]*]+\\s+" + backtick + "([^" + backtick + "]*)" + backtick)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		currentStruct := ""
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // 注释里会引用 default:true 作为反例说明
			}
			if sm := structPat.FindStringSubmatch(trimmed); sm != nil {
				currentStruct = sm[1]
				continue
			}
			if trimmed == "}" {
				currentStruct = ""
				continue
			}
			m := fieldPat.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			field, tag := m[1], m[2]
			dm := nonZeroDefaultPattern.FindStringSubmatch(tag)
			if dm == nil {
				continue
			}
			val := strings.TrimSpace(dm[1])
			if val == "0" || val == "'0'" || val == "false" || val == "''" {
				continue // 零值默认不会丢数据
			}
			key := e.Name() + ":" + field
			if currentStruct != "" {
				key = e.Name() + ":" + currentStruct + "." + field
			}
			out[key] = val
		}
	}
	return out
}

// TestNonZeroDefaultsAreRegistered 钉住「非零 default 必须显式登记」。
func TestNonZeroDefaultsAreRegistered(t *testing.T) {
	found := collectNonZeroDefaults(t, ".")

	// 分母守卫：必须真的扫到东西，否则门禁是空转
	if len(found) < 40 {
		t.Fatalf("只扫到 %d 个非零 default 字段（期望 >=40）—— 判据/正则被改坏，门禁形同虚设", len(found))
	}

	var unregistered, stale []string
	for k, v := range found {
		if _, ok := registeredNonZeroDefaults[k]; !ok {
			unregistered = append(unregistered, k+" (default:"+v+")")
		}
	}
	for k := range registeredNonZeroDefaults {
		if _, ok := found[k]; !ok {
			stale = append(stale, k)
		}
	}
	sort.Strings(unregistered)
	sort.Strings(stale)

	if len(unregistered) > 0 {
		t.Errorf("新增了未登记的非零 default（请回答「显式零值对该字段有语义吗」并登记到 registeredNonZeroDefaults）：\n  %s",
			strings.Join(unregistered, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("登记表里的条目已不存在（tag 被删或字段改名，请同步删除，防登记表腐烂）：\n  %s",
			strings.Join(stale, "\n  "))
	}
}
