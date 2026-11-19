package api

import "ehome/backend/internal/hwid"

// bind_to 的校验分两层，规范值的**形状与集合**统一由 internal/hwid 提供
// （唯一真源：启动迁移 internal/database 也用同一份，避免两处漂移）：
//
//	ShapeOK   —— 形状是否是固件 derive_hw_id() 可能产出的，且放得进 16 字节缓冲；
//	Canonical —— 与某个规范 id 是否**仅大小写不同**（是则返回正确写法）。
//
// 为什么必须校验值（2026-10-04 实错）：S3 的 node.Config 长期存着
//
//	{"dma_id": 1, "bind_to": "uart/uart1", "enabled": true}
//
// 而固件派生的规范 hw_id 是 "uart/UART1"（只有大小写不同）。固件侧
// dma_pool_apply_config 不校验、直接标 ALLOCATED 并写入 bound_to，于是：
//   - 预抢占（preempt）用 strcmp 比对，永远匹配不上 -> 占不回那条通道；
//   - dma_pool_release_by_hw 同样 strcmp -> 永远释放不掉。
//
// 结果是该 GDMA 通道被**永久占用**：S3 只有 5 条 GDMA，占掉 1 条后
// 「3 UART + SPI2 + I2C0」就分不出 DMA，I2C0 报 "No DMA for i2c/I2C0"，
// 整份 ConfigManifest 以 ESP_ERR_NOT_FOUND 被拒、success=false。
//
// 刻意**不静默改写**：用户看到的值与系统实际用的值不一致，正是本次事故的
// 成因；静默纠正会让同样的错配再次隐形成立。想修复存量数据请走
// internal/database 的迁移（那里会把每一处改写记进日志，可审计）。
func hwIDShapeOK(s string) bool { return hwid.ShapeOK(s) }

func hwIDCaseSuggestion(s string) string { return hwid.Canonical(s) }
