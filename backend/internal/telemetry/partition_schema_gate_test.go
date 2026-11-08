package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// 本文件钉住方案 §8 风险表的最后一条：
//
//	「PG schema 不在 AutoMigrate ⇒ 生产与测试 schema 漂移
//	  ⇒ 同改 partitionParentDDL / partitionParentCopyColumns / 索引模板 / GORM models 四处」
//
// 为什么用**源码扫描**而不是跑 DDL：分区母表的建表路径只在 PG 上生效，
// 而单测跑 SQLite（datalifecycle 的 PG 用例需要外部 PG）。若这里也依赖 PG，
// 这条门禁在绝大多数环境里会静默 skip —— 那等于没有门禁。
// 扫源码至少能保证"四处都改了"，而这正是漏改的真实形态（改一处忘三处）。
// 与 internal/database/operation_logs_gate_test.go 同一手法。
func TestPartitionSchemaRegisteredForFrameTable(t *testing.T) {
	path := filepath.Join("..", "datalifecycle", "partition_mgr.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(raw)

	// 1) 建表模板必须登记，且必须是分区表、RANGE(ts)。
	ddl := extractMapEntry(t, src, "var partitionParentDDL = map[string]string{", "\"telemetry_frame\":")
	for _, want := range []string{
		"device_id", "ts", "seq", "frame_kind", "slot_id", "payload", "raw",
		"PRIMARY KEY (device_id, ts, seq)",
		"PARTITION BY RANGE (ts)",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("partitionParentDDL[telemetry_frame] 缺少 %q —— 生产建表与模型会漂移", want)
		}
	}

	// 2) 索引模板必须登记，且**只有**关键帧部分索引（方案 §4.1「只保留 2 条索引」：
	//    主键由 DDL 建，模板里再多一条就违反方案）。
	idx := extractMapEntry(t, src, "var partitionParentIndexes = map[string][]string{", "\"telemetry_frame\":")
	n := strings.Count(idx, "CREATE INDEX")
	if n != 1 {
		t.Errorf("partitionParentIndexes[telemetry_frame] 有 %d 条索引，方案 §4.1 要求 1 条（关键帧部分索引）", n)
	}
	for _, want := range []string{"(device_id, ts)", "WHERE frame_kind = 1"} {
		if !strings.Contains(idx, want) {
			t.Errorf("关键帧部分索引缺少 %q（LatestSnapshot 要「该设备最后一个关键帧」）", want)
		}
	}

	// 3) 迁移列清单必须登记（未登记会回退到 unified_data 的列清单，列根本不存在）。
	cols := extractMapEntry(t, src, "var partitionCopyColumns = map[string]string{", "\"telemetry_frame\":")
	for _, want := range []string{"device_id", "ts", "seq", "frame_kind", "slot_id", "payload", "raw"} {
		if !strings.Contains(cols, want) {
			t.Errorf("partitionCopyColumns[telemetry_frame] 缺少列 %q", want)
		}
	}

	// 4) GORM 模型必须与 DDL 的主键/列同名同义。
	frame := models.TelemetryFrame{}
	if got := frame.TableName(); got != "telemetry_frame" {
		t.Errorf("模型表名 = %q, want telemetry_frame", got)
	}
	slot := models.TelemetrySlot{}
	if got := slot.TableName(); got != "telemetry_slot" {
		t.Errorf("模型表名 = %q, want telemetry_slot", got)
	}
}

// extractMapEntry 从 src 里 locator 所在位置起，取到下一个 "\n\t}" 或文件结束。
//
// 只用于**门禁断言**，容忍注释里出现关键字 —— 它判的是"这一段里有没有这些东西"，
// 不是精确解析 Go 语法（引号/转义在原始 map 字面量里形态稳定）。
func extractMapEntry(t *testing.T, src, mapHeader, key string) string {
	t.Helper()
	start := strings.Index(src, mapHeader)
	if start < 0 {
		t.Fatalf("找不到 map 定义 %q（被改名了？门禁失效）", mapHeader)
	}
	rest := src[start:]
	keyStart := strings.Index(rest, key)
	if keyStart < 0 {
		t.Fatalf("%s 未登记 %s —— 该表没有建表/索引/迁移路径", mapHeader, key)
	}
	rest = rest[keyStart:]
	// map 条目以「换行 + 单个右花括号」结尾；这里用字节判断而不是字符串字面量，
	// 免得源码里的换行转义在两次转义中走样。
	if i := strings.Index(rest, "\n}"); i >= 0 {
		return rest[:i]
	}
	return rest
}
