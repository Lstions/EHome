package api

// device_purger_hook.go — 后台「设备数据清理任务」的注入点。
//
// 为什么需要（2026-10-25 批量管理/历史设备）：
// 历史设备的「彻底删除」只是把 data_purge_requested 置 true，真正硬删由
// datalifecycle.DevicePurger 后台分批执行（默认启动后 30s 首跑、之后每 24h）。
// 用户点「彻底删除」后如果只能等定时任务，最长要等 24h 才真正清掉 —— 体验上
// 等于"删除不生效"。API 提交入队后立即 Wake() 唤醒一轮清理。
//
// 用注入而不是改 registerEdgeDeviceRoutes 签名：与 SetRuleSourceGroupService 同一
// 模式（main.go 一行接线），避免为了一个后台任务把既有注册链路全部改签名。
// 未接线时（单元测试/精简嵌入）保持 nil，Wake 静默跳过 —— 清理仍会由定时任务完成。

import (
	"sync"

	"ehome/backend/internal/datalifecycle"
)

var (
	devicePurgerMu sync.RWMutex
	devicePurger   *datalifecycle.DevicePurger
)

// SetDevicePurger 注入后台清理任务（main.go 在 purger.Start() 之后调用一次）。
func SetDevicePurger(p *datalifecycle.DevicePurger) {
	devicePurgerMu.Lock()
	devicePurger = p
	devicePurgerMu.Unlock()
}

// wakeDevicePurger 请求后台清理任务立即跑一轮（非阻塞；未接线时 no-op）。
func wakeDevicePurger() {
	devicePurgerMu.RLock()
	p := devicePurger
	devicePurgerMu.RUnlock()
	if p != nil {
		p.Wake()
	}
}
