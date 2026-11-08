package databus

// S3 量化探针 (2026-10-07)。
//
// 审计 S3 断言: 同一个 DataEvent 会被**两个**消费者各写一行 device_data。
// 本文件先把这件事**测出来**, 再谈修法 —— 没有测量就动手等于改一个自己没确认的重复。
//
// 探针走**真实总线** (DataEventBus.Publish → fanout → 两个消费者), 不直接调 Handle,
// 否则就绕过了"两个消费者都会收到同一个事件"这个待验证的前提。
//
// 结论 (被下方用例钉住):
//   · 可解析事件 → 2 行 (两个消费者各一行, 形状不同);
//   · 三类不可解析事件 → **只有 1 行** (db_persist), 即两个写入者覆盖面不同,
//     不是纯冗余 —— 这是 Lead task-16 裁决"两个写入者都保留"的依据。
//
// ============ 审计导航（读这一节就够，不用看 git history）============
//
// **本文件只有 Test 函数，没有夹具。** 夹具在 s3_fixture_test.go。
//
// 历史（2026-11-08）：原来夹具和这 3 条探针住在同一个文件里。
// 发现过程：清理 dead 文件时判断「它是不是纯基础设施」，只查了「被谁依赖」
// （结论：task19 用例依赖 newS3Fixture）⇒ 据此打算整份改名成 s3_fixture_test.go。
// **但那个判断漏了第二个问题：「它自己断言了什么」。**
// 查下去才发现里面有 3 条独一无二的探针 —— 整份改名会让门禁藏进 fixture 文件，
// 而「fixture 文件里不会有重要断言」这个直觉比「probe 文件可能是基础设施」更强，
// 等于把一个命名谎言换成另一个。故改为拆分。
//
// 顺带纠正一个曾经写错的注释：曾有一版说这里「已是纯基础设施」，那是错的。
//
// 夹具（newS3Fixture / s3WiredBus / s3WaitForRows / s3BarrierConsumer …）已移到
// s3_fixture_test.go —— 它们被 device_data_write_shape_task19_test.go 使用，
// 属于测试基础设施，放在带 probe 字样的文件里会让下一个人以为可以随手删掉
// （实测移走直接编译失败：undefined: newS3Fixture）。本文件只保留**断言**。
//
// 等待一律**确定性**: 不靠固定 Sleep。见 s3WaitForRows 与 s3BarrierConsumer。

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// TestS3_Probe_OneEventWritesTwoRows 是 S3 的**量化测量**。
//
// 一个可解析 DataEvent 之后 device_data 恰好 2 行, 且两行形状不同、
// DeviceID 语义不同 (一个 0, 一个真实设备 id)。测量结果同时打印出来供报告引用。
func TestS3_Probe_OneEventWritesTwoRows(t *testing.T) {
	f := newS3Fixture(t)
	bus, barrier := s3WiredBus(t, f)

	bus.Publish(s3Sample(f))

	// 确定性屏障: 等解析者 Handle 返回 (它一定会被调用 —— ShouldParse 为真)。
	select {
	case <-barrier.done:
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内解析者未完成 Handle —— 无法确定性断言行数")
	}

	rows := s3WaitForRows(t, f.db, 2, "可解析事件应写 2 行")
	t.Logf("=== S3 测量: 1 个 DataEvent → %d 行 device_data ===", len(rows))
	for i, row := range rows {
		t.Logf("row[%d] id=%d device_id=%d node_id=%q",
			i, row.ID, row.DeviceID, row.NodeID)
		t.Logf("        data_json=%s", row.DataJSON)
		t.Logf("        data_json 长度=%d B", len(row.DataJSON))
	}

	// 两个写入者形状必须不同, 否则"重复"可能只是无害的镜像。
	if rows[0].DataJSON == rows[1].DataJSON {
		t.Fatalf("两行 data_json 完全相同, 不是审计描述的双形状: %s", rows[0].DataJSON)
	}
	// DeviceID 语义必须不同: db_persist 不设 DeviceID(0), sensor_parser 设真实 id。
	zeroCount := 0
	for _, row := range rows {
		if row.DeviceID == 0 {
			zeroCount++
		}
	}
	if zeroCount != 1 {
		t.Fatalf("期望恰好 1 行的 device_id=0, 实际 %d 行", zeroCount)
	}
	t.Logf("=== 测量结论: 1 事件 → 2 行, 形状不同, DeviceID 语义不同 ===")
}

// TestS3_Probe_NonParseableEventWritesOnlyOneRow 是 S3 的**反面测量**。
//
// 证明"两类不可解析事件只写 1 行"。这是"两个写入者覆盖面不同、不是纯冗余"的
// 直接证据, 也是"不能删 DBPersistConsumer"的依据。
//
// 确定性: 对 ShouldHandle 为假的输入, 解析者**根本不会被调用** —— 这一点由
// 本用例直接断言 barrier.ShouldHandle(evt)==false 来确立 (纯函数, 无时序),
// 因此"它不会写"不依赖任何等待。
func TestS3_Probe_NonParseableEventWritesOnlyOneRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*DataEvent)
	}{
		{
			name: "error_code != 0 (关键样本, 0x03 独立上报)",
			mut:  func(e *DataEvent) { e.ErrorCode = 0x1100 },
		},
		{
			name: "raw_data 为空 (无可解析载荷)",
			mut:  func(e *DataEvent) { e.RawData = nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newS3Fixture(t)
			bus, barrier := s3WiredBus(t, f)

			evt := s3Sample(f)
			tc.mut(&evt)

			// ① 先确立"解析者不会被调用" —— 纯函数判定, 无时序假设。
			if barrier.ShouldHandle(evt) {
				t.Fatalf("本用例前提被破坏: 该输入下 ShouldParse() 应为假, 否则不构成只有 db_persist 会写的情形")
			}

			bus.Publish(evt)

			// ② db_persist 那一行必然出现 (ShouldPersist 为真) —— 确定性轮询。
			rows := s3WaitForRows(t, f.db, 1, "不可解析事件应写 1 行 (仅 db_persist)")

			// ③ 解析者不可能被调用 (①), 故此刻行数仍为 1 即为定论。
			if again := deviceDataRows(t, f.db); len(again) != 1 {
				t.Fatalf("行数从 1 变为 %d —— 解析者在 ShouldHandle=false 时仍写入了", len(again))
			}

			t.Logf("=== S3 测量[%s]: 1 个 DataEvent → %d 行 device_data ===", tc.name, len(rows))
			for i, row := range rows {
				t.Logf("  row[%d] device_id=%d data_json=%s", i, row.DeviceID, row.DataJSON)
			}
			if rows[0].DeviceID != 0 {
				t.Fatalf("唯一那行应来自 db_persist (device_id=0), 实际 device_id=%d", rows[0].DeviceID)
			}
			select {
			case <-barrier.done:
				t.Fatal("解析者屏障被触发, 与 ShouldHandle=false 矛盾")
			default:
			}
		})
	}
}

// TestS3_Probe_UnparseableByDriverWritesOnlyOneRow 覆盖"ShouldHandle 为真但解析被拒"。
//
// 这类事件 ShouldParse() 为真 (解析者**会**被调用), 但在 Handle 内部因校准缺失
// 提前 return —— 结果同样只有 db_persist 那一行。
//
// 它说明: 即便把 DBPersistConsumer.ShouldHandle 收窄成 !ShouldParse(),
// 也无法覆盖这一类, 因为"是否写入"取决于 Handle 内部的运行期结果。
//
// 确定性: 这类**会**调用解析者, 所以用 barrier.done 做屏障 (Handle 返回即写完),
// 不靠时长猜测。
func TestS3_Probe_UnparseableByDriverWritesOnlyOneRow(t *testing.T) {
	f := newS3Fixture(t)
	// 删掉校准: bmp280 是校准感知驱动, 缺失即拒收 (consumers_heavy.go 校准门)。
	if err := f.db.Where("edge_device_id = ?", f.device.ID).Delete(&models.CalibrationCache{}).Error; err != nil {
		t.Fatal(err)
	}
	bus, barrier := s3WiredBus(t, f)

	evt := s3Sample(f)
	// 前提: 该输入下解析者**会**被调用 (否则测的不是这一档)。
	if !barrier.ShouldHandle(evt) {
		t.Fatal("本用例前提被破坏: 校准缺失时 ShouldParse() 应为真, 否则覆盖不到 Handle 内拒收这一档")
	}

	bus.Publish(evt)

	// 等解析者 Handle 返回 (它内部已因校准缺失提前 return)。
	select {
	case <-barrier.done:
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内解析者未完成 Handle —— 无法确定性断言行数")
	}

	rows := s3WaitForRows(t, f.db, 1, "校准缺失时解析被拒, 应只写 1 行")
	t.Logf("=== S3 测量[校准缺失, Handle 内拒收]: 1 个 DataEvent → %d 行 device_data ===", len(rows))
	for i, row := range rows {
		t.Logf("  row[%d] device_id=%d data_json=%s", i, row.DeviceID, row.DataJSON)
	}
}
