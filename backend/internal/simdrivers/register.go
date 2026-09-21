//go:build simulation

// Package simdrivers 登记**仅用于场景仿真**的驱动型号。
//
// 背景（2026-09-21 门禁收紧）：POST /device-configs 现在要求 device_type 能在
// drivers.Registry 中解析，地址门禁对"未注册型号"也改为 fail-closed。仿真框架
// 过去刻意使用**未注册**型号（sim_generic / sim_scene_*_*），靠 registry.Get
// 失败来保证解析走 DeviceConfig.Parser、不碰驱动协议分支。收紧之后那条路被堵死。
//
// 这个包用**构建标签**（而不是放宽生产门禁）解决冲突：
//   - 生产二进制不编译本包（cmd/server 的 registerSimulationDriverTypes 在
//     !simulation 构建下是空实现），因此生产上"型号必须已注册"这条规则没有任何
//     后门；
//   - 仿真子进程用 go build -tags=simulation 编译（harness/build 里指定），与
//     测试进程读到的是同一份清单，不存在两侧不同步。
//
// 注册进来的型号与收紧前的可观测行为逐项一致：
//   - ParseData 一律失败 → 数据解析仍走场景自己的 DeviceConfig.Parser；
//   - GetCommandTemplates 返回 nil → 不生成 ConfigTemplate，上报节奏由场景掌控；
//   - 不实现任何 ControlAction* 接口 → 不产生动作、不需要目标地址，
//     因此地址门禁对它们"已收录但不需要地址"（合法放行任意标识符，
//     与内置的 generic_i2c / 通用型号同档）。
package simdrivers

import (
	"fmt"

	"ehome/backend/internal/drivers"
)

// parserOnlyDriver 是一个"只作为型号存在"的驱动：没有协议解析、没有命令模板、
// 没有控制动作，一切具体行为都由绑定到该型号的 DeviceConfig 描述。
type parserOnlyDriver struct{ typeID string }

func (d *parserOnlyDriver) DeviceType() string      { return d.typeID }
func (d *parserOnlyDriver) DeviceName() string      { return "仿真专用型号（解析走 DeviceConfig.Parser）" }
func (d *parserOnlyDriver) OEM() string             { return "仿真" }
func (d *parserOnlyDriver) Category() string        { return "仿真设备" }
func (d *parserOnlyDriver) HardwareTypes() []string { return []string{"uart", "i2c", "spi", "adc"} }

func (d *parserOnlyDriver) GetSensorDefinitions() []drivers.SensorData { return nil }

// ParseData 一律失败：仿真设备的字节布局由场景自己的 DeviceConfig.Parser 描述。
func (d *parserOnlyDriver) ParseData([]byte) ([]drivers.SensorData, error) {
	return nil, fmt.Errorf("%s: 仿真型号没有内置协议解析器，请使用 DeviceConfig.Parser", d.typeID)
}

// types 是全部仿真专用型号。
//
// 口径（2026-09-21 实测，可复跑 .fix4/extract_types.py）：backend/simulation
// 全目录里出现在**设备型号位**的字面量 ——
//   · autoProvisionDevice(e, scenarioID, suffix, deviceType) 的第 4 实参；
//   · edgeDeviceSpec.DeviceType；
//   · sceneSpec.Type；
//   · harness/fixture.go 的 fixtureDeviceType（sim_generic）。
//
// 减去内置驱动注册表里已有的型号（sn3001_rain / jiabaida_bms 已注册）。
// 当前共 47 个需要登记。
//
// 新增仿真型号而忘了登记时，simulation/sim_drivers_test.go 的静态门禁会直接变红
// （该用例扫描上面四种位置，并带下界断言与分类器自检）。
var types = []string{
	"sim_generic",         // harness/fixture.go：通用夹具
	"sim_scene_001_light", // catalog/scene.go SIM-SCENE-001
	"sim_scene_002_pv",    // catalog/scene.go SIM-SCENE-002
	"sim_scene_008_pv",    // catalog/scene.go SIM-SCENE-008
	// 其余为各场景通过 autoProvisionDevice(..., deviceType) 声明的自定义型号。
	// 逐个显式登记（而不是按前缀通配），让"某个场景用了什么型号"在一处可读。
	"sim_alert_001_sensor", "sim_alert_002_sensor", "sim_alert_003_sensor", "sim_alert_004_sensor",
	"sim_audt_001_sensor", "sim_audt_003_sensor", "sim_audt_005_sensor",
	"sim_auto_001_sensor", "sim_auto_002_sensor", "sim_auto_003_sensor", "sim_auto_006_sensor",
	"sim_cond_001_sensor", "sim_cond_002_sensor", "sim_cond_003_sensor", "sim_cond_004_sensor",
	"sim_crud_001_sensor", "sim_crud_002_sensor", "sim_crud_003_sensor",
	"sim_crud_004_sensor", "sim_crud_005_sensor", "sim_crud_006_sensor",
	"sim_dbln_001_sensor", "sim_dbln_002_sensor", "sim_dbln_003_sensor",
	"sim_err_004_sensor",
	"sim_manu_001_sensor", "sim_manu_004_sensor",
	"sim_ntfy_001_sensor", "sim_ntfy_002_sensor", "sim_ntfy_003_sensor",
	"sim_ntfy_004_sensor", "sim_ntfy_005_sensor", "sim_ntfy_006_sensor", "sim_ntfy_007_sensor",
	"sim_trig_001_sensor", "sim_trig_002_sensor", "sim_trig_003_sensor", "sim_trig_004_sensor",
	"sim_trig_005_other", "sim_trig_005_watched",
	"sim_trig_006_sensor", "sim_trig_007_sensor", "sim_trig_008_sensor",
}

// Types 返回仿真专用型号清单的副本（门禁用例与注册共用同一份清单）。
func Types() []string { return append([]string(nil), types...) }

// Register 把仿真专用型号补进注册表。registry 为 nil 时不做任何事。
func Register(registry *drivers.Registry) {
	if registry == nil {
		return
	}
	for _, t := range types {
		registry.Register(&parserOnlyDriver{typeID: t})
	}
}
