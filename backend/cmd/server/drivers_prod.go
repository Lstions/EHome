//go:build !simulation

package main

import "ehome/backend/internal/drivers"

// registerSimulationDriverTypes 是生产构建下的空实现。
//
// 仿真专用型号（sim_generic / sim_scene_*_*）只存在于 -tags=simulation 构建
// （drivers_sim.go + internal/simdrivers）。生产二进制里没有这个后门：任何
// device_type 都必须在内置驱动注册表中真实存在，否则 POST /device-configs
// 拒绝创建、地址门禁对该型号 fail-closed。
func registerSimulationDriverTypes(*drivers.Registry) {}
