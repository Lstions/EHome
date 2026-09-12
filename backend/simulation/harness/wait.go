//go:build simulation

package harness

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	// defaultPollInterval 是 Eventually 的缺省轮询间隔。
	// 设计 §3 原则 3：所有时序断言走轮询，禁止用 sleep 同步断言。
	defaultPollInterval = 200 * time.Millisecond
	// framePollInterval 是 AwaitFrame 的轮询间隔：MQTT 帧在 paho 回调
	// goroutine 内入队，到达时刻不可预知，只能轮询等待。
	framePollInterval = 25 * time.Millisecond
)

// EventuallyEveryError 轮询 cond 直到返回 nil，或超过 timeout。
// 超时返回的错误里带上轮询次数与最后一次失败原因——这是场景失败时
// 唯一能定位"卡在哪一步"的信息。
func (e *Env) EventuallyEveryError(timeout, interval time.Duration, cond func() error) error {
	if interval <= 0 {
		interval = defaultPollInterval
	}
	if cond == nil {
		return fmt.Errorf("Eventually 需要非 nil 的 cond")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	attempts := 0
	var last error
	for {
		attempts++
		if last = cond(); last == nil {
			return nil
		}
		select {
		case <-timer.C:
			return fmt.Errorf("等待 %s（%d 次轮询）后条件仍未满足: %w", timeout, attempts, last)
		case <-ticker.C:
		}
	}
}

// EventuallyError 是 Eventually 的非致命版本，供夹具自检与需要分支的
// 场景使用（例如"等 3s 看是否出现"）。
func (e *Env) EventuallyError(timeout time.Duration, cond func() error) error {
	return e.EventuallyEveryError(timeout, defaultPollInterval, cond)
}

// Eventually 轮询直到 cond 返回 nil；超时即失败当前场景。
// 契约见设计 §5.1（Env.Eventually）。
func (e *Env) Eventually(timeout time.Duration, cond func() error) {
	e.T.Helper()
	if err := e.EventuallyError(timeout, cond); err != nil {
		e.Fatalf("Eventually 断言失败: %v", err)
	}
}

// EventuallyCount 轮询场景库计数条件，用于"界面看不到的持久化事实"
// 的收敛等待（设计 §3 原则 2-b）。query 必须是 SELECT count(*) ...。
func (e *Env) EventuallyCount(timeout time.Duration, query string, args []any, want func(int64) error) {
	e.T.Helper()
	ctx, cancel := e.commandContext(timeout + 5*time.Second)
	defer cancel()
	e.Eventually(timeout, func() error {
		var got int64
		if err := e.SQL().QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
			return fmt.Errorf("计数查询失败 (%s): %w", query, err)
		}
		return want(got)
	})
}

// eventuallyRows 是内部小工具：轮询一组只读 SQL，直到扫描函数返回 nil。
func (e *Env) eventuallyRows(timeout time.Duration, query string, args []any, scan func(*sql.Rows) error) error {
	return e.EventuallyError(timeout, func() error {
		ctx, cancel := e.commandContext(10 * time.Second)
		defer cancel()
		rows, err := e.SQL().QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("查询失败 (%s): %w", query, err)
		}
		defer rows.Close()
		return scan(rows)
	})
}
