package datalifecycle

// purger_wake_test.go — DevicePurger.Wake() 的机制测试。
//
// 背景（2026-10-25 批量管理/历史设备）：历史设备「彻底删除」只是把
// data_purge_requested 置 true，真正硬删由每日 purge 任务执行（最长 24h）。
// API 提交入队后调用 Wake() 立即跑一轮；Wake 必须**非阻塞**、多次请求**合并**
// 成一个 token（不能因为连点而堆积无意义的重复清理）。

import (
	"testing"

	"ehome/backend/testutil"
)

func TestDevicePurgerWakeEnqueuesSingleToken(t *testing.T) {
	db := testutil.OpenTestDB(t)
	p := NewDevicePurger(db)

	select {
	case <-p.wakeCh:
		t.Fatal("wakeCh should start empty")
	default:
	}

	p.Wake()
	p.Wake()
	p.Wake()

	select {
	case <-p.wakeCh:
	default:
		t.Fatal("Wake must enqueue a token so the run loop wakes immediately")
	}
	select {
	case <-p.wakeCh:
		t.Fatal("Wake must coalesce concurrent requests into a single pending token")
	default:
	}
}
