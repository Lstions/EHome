//go:build simulation

package commandexec

// command_deadline_sim.go — 仿真构建专用的 Deadline 覆盖（生产二进制不含本文件）。
//
// harness 通过 EHOME_SIM_COMMAND_DEADLINE 注入（见 simulation/harness/env.go）。
// 用 build tag 而不是生产 env：现场没有任何理由改命令超时，而一个可改的超时
// 会让「执行多久算失败」在不同部署间悄悄不同。

import (
	"os"
	"time"

	"ehome/backend/pkg/logger"
)

const simCommandDeadlineEnv = "EHOME_SIM_COMMAND_DEADLINE"

func init() {
	raw := os.Getenv(simCommandDeadlineEnv)
	if raw == "" {
		return
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.Warnf("simulation: 忽略非法的 %s=%q（应为正 duration）", simCommandDeadlineEnv, raw)
		return
	}
	commandDeadline = d
}
