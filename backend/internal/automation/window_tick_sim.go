//go:build simulation

package automation

// window_tick_sim.go — 仿真构建专用的求值周期覆盖（**生产二进制不含本文件**）。
//
// 为什么用 build tag 而不是环境变量：这个覆盖的唯一用途是让 140 个场景不必各自
// 真等 30~180s；把它放进生产 env，就等于给现场一个「把自动化 ticker 调到 2s」
// 的旋钮 —— 没人需要，却能悄悄改变生产行为。
//
// harness 通过 EHOME_SIM_WINDOW_TICK 注入（见 simulation/harness/env.go）。

import (
	"os"
	"time"

	"ehome/backend/pkg/logger"
)

const simWindowTickEnv = "EHOME_SIM_WINDOW_TICK"

func init() {
	raw := os.Getenv(simWindowTickEnv)
	if raw == "" {
		return
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.Warnf("simulation: 忽略非法的 %s=%q（应为正 duration，如 2s）", simWindowTickEnv, raw)
		return
	}
	windowTickInterval = d
}
