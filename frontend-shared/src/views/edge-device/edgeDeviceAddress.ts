/**
 * 边缘设备**地址能力**的唯一判定口径（纯函数模块，零 Vue 依赖）。
 *
 * 2026-11-12 第三轮拆分：从 composables/useEdgeDeviceCreate.ts 原样搬出，
 * 因为这两件事都是**与 UI 状态无关的纯判定**，值得单独被单测直接钉住：
 *   · resolveModelRequiresTargetAddress —— 型号是否消费设备地址（三分支，不能压成布尔）；
 *   · resolveInlineDeviceAddress      —— 把「通道的总线资源标识」解析成可提交的设备地址。
 *
 * 为什么判定必须来自后端能力字段而不是"字符串形态"：后端 validateEdgeDeviceAddress 只对
 * driverRequiresTargetAddress 为真的型号校验 1-254，纯 I2C/SPI 型号原样接受任何标识；
 * 按形态判断就会给后者凭空写一个它根本不用的地址 1。
 *
 * 门禁：EdgeDeviceAddressCapabilityGate / EdgeDeviceAddressSemanticsGate 的源码断言
 * 现在把本文件与页面、创建对话框、创建组合式一起拼接读取（断言文本一字未改）。
 */
import { findDriverLeaf, type DriverTreeNode } from '@/api/driver'
import { DEVICE_ADDRESS_DEFAULT, isValidDeviceAddress, parseDeviceAddress } from '@/utils/deviceAddress'

/**
 * 该型号是否消费设备地址。三分支（**必须**保持可区分，不能压成布尔）：
 *   true      → 要求地址；
 *   false     → 明确不要求；
 *   undefined → 拿不到能力字段（老后端 / 树未加载完 / 该型号只在 DB 模板里）
 *               ⇒ 调用方退回"字段缺失"的兼容行为。
 *
 * findDriverLeaf 是 @/api/driver 暴露的唯一查询入口，避免各处 find 出不同口径。
 */
export function resolveModelRequiresTargetAddress(
  driverTree: DriverTreeNode[],
  deviceType: string | undefined,
): boolean | undefined {
  const leaf = findDriverLeaf(driverTree, String(deviceType ?? '').trim())
  return typeof leaf?.requires_target_address === 'boolean' ? leaf.requires_target_address : undefined
}

/**
 * D6(c) + m-1（2026-09-22）：把「通道的总线资源标识」解析成可提交的
 * **EdgeDevice.hardware_id（设备地址）**。
 *
 * 语义边界（本次 UI 缺陷族的根因族）：
 *   · channel.hardware_id       = 总线资源标识（UART0 / SPI2 / I2C0）
 *   · edgeDevice.hardware_id    = 该设备在总线上的**地址**（Modbus 从站号 / I2C 地址，1-254）
 * 判定口径不在这里自造：复用 @/utils/deviceAddress（与后端 deviceaction.ParseHardwareAddress
 * 同一张表，由 DeviceAddressContractParity.spec.ts 双向钉死）。
 *
 * m-1：**是否要求地址由型号能力决定，而不是由字符串形态决定**。后端
 * validateEdgeDeviceAddress 只对 driverRequiresTargetAddress 为真的型号校验 1-254，
 * 纯 I2C/SPI 型号原样接受任何标识；改前这里一律按形态兜底成默认地址 1，于是给
 * 一个从不使用地址的型号凭空写了一个地址。三分支：
 *   · requires === true      → 合法地址原样提交；总线名 ⇒ 默认地址 1 **并显式提示**；
 *   · requires === false     → **不造地址**：提交空串（后端对空值的语义是「无地址/默认」），
 *                              也不弹无意义提示；
 *   · requires === undefined → 兼容分支（老后端不返回该字段、树未加载完、或型号只在
 *                              DB 模板里）：保持**改前行为**（默认地址 1 + 提示），
 *                              宁可与后端多校验一次，也不静默放行总线名。
 */
export function resolveInlineDeviceAddress(
  busHardwareId: string | undefined,
  requiresTargetAddress?: boolean,
): { value: string; warned: boolean } {
  const raw = String(busHardwareId ?? '').trim()
  const valid = isValidDeviceAddress(raw)
  const explicitValue = valid && parseDeviceAddress(raw).explicit ? raw : ''
  // 明确不消费地址的型号：不要凭空造一个它不用的地址（这是本任务的核心修复）。
  if (requiresTargetAddress === false) {
    return { value: '', warned: false }
  }
  if (valid) {
    return { value: explicitValue, warned: false }
  }
  // requires === true（真源要求地址）或 === undefined（能力未知，兼容旧后端）：
  // 退回后端默认地址 1，并由调用方显式提示。
  return { value: String(DEVICE_ADDRESS_DEFAULT), warned: true }
}
