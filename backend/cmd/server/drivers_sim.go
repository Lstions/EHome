//go:build simulation

package main

import (
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/simdrivers"
)

// registerSimulationDriverTypes 只在 -tags=simulation 构建下存在，把仿真专用
// 型号（sim_generic / sim_scene_*_*）补进驱动注册表。
//
// 为什么需要：仿真 harness 启动的是**真实组合根**，而收紧后的门禁要求
// device_type 必须已注册。仿真框架用 DeviceConfig.Parser 描述数据布局，不需要
// 驱动解析，所以这些型号以"只作为型号存在"的驱动登记。
//
// 为什么生产不受影响：本文件的 !simulation 版本（drivers_prod.go）是空实现，
// 生产镜像的 go build 不带该标签（见 Dockerfile），仿真型号在产物里根本不存在。
func registerSimulationDriverTypes(registry *drivers.Registry) {
	simdrivers.Register(registry)
}
