/**
 * 驱动树快照（型号 → 是否消费设备地址 的唯一数据源）。
 *
 * 为什么单独成模块：这张快照**两个域在用** ——
 *   · 页面卡片「从机地址」栏（addressValue）只读它判断该型号是否真的有地址；
 *   · 创建/编辑组合式在提交时按型号能力决定发不发地址。
 * 两处必须看同一份快照，否则会出现「卡片显示有地址、提交按无地址处理」的口径漂移；
 * 由页面创建一次、再把 { fetchDriverTreeOnce, resolveModelRequiresTargetAddress } 传给
 * useEdgeDeviceCreate，即可保证只有一份。
 *
 * 判定本体是纯函数（../edgeDeviceAddress.ts），本模块只负责取数与缓存。
 */
import { ref } from 'vue'
import { getDriverTree, type DriverTreeNode } from '@/api/driver'
import { logger } from '@/utils/logger'
import { resolveModelRequiresTargetAddress as resolveModelAddressCapability } from '../edgeDeviceAddress'

export function useDeviceDriverTree() {
  // ── m-1（2026-09-22）：型号「是否消费设备地址」的能力来源 ─────────────
  // 唯一数据源就是 /device-configs/tree 的驱动叶子字段 requires_target_address
  // （与后端地址门禁 driverRequiresTargetAddress 同一口径）。这里**不新造**第二个
  // 数据源，也不在本文件重写一份判定表：Tree → 型号 → 能力，全程同一张快照。
  //
  // 为什么能力必须来自后端而不是"字符串形态"：后端只对**真的解析地址**的型号
  // （纯 Modbus 类）校验 1-254；纯 I2C/SPI 采集型号原样接受任何标识。按形态判断
  // 就会给后者凭空写一个它根本不用的地址 1 —— 正是本次要修的口径不一致。
  const driverTree = ref<DriverTreeNode[]>([])
  const driverTreeLoaded = ref(false)

  /**
   * 该型号是否消费设备地址。三分支（**必须**保持可区分，不能压成布尔）：
   *   true      → 要求地址；
   *   false     → 明确不要求；
   *   undefined → 拿不到能力字段（老后端 / 树未加载完 / 该型号只在 DB 模板里）
   *               ⇒ 调用方退回"字段缺失"的兼容行为。
   *
   * 判定实现在 edgeDeviceAddress.ts（findDriverLeaf 是 @/api/driver 暴露的唯一查询入口），
   * 这里只把当前快照喂进去，避免各处 find 出不同口径。
   */
  const resolveModelRequiresTargetAddress = (deviceType: string | undefined): boolean | undefined =>
    resolveModelAddressCapability(driverTree.value, deviceType)

  /**
   * 拉取一次驱动树快照（m-1）。失败时保持 driverTreeLoaded=false，并在下次打开
   * 向导时重试 —— 不缓存"失败"这种状态，否则一次网络抖动会让能力查询在整个
   * 页面生命周期内都拿不到值。
   */
  const fetchDriverTreeOnce = async () => {
    if (driverTreeLoaded.value) return
    try {
      driverTree.value = await getDriverTree()
      driverTreeLoaded.value = true
    } catch (error) {
      logger.warn('获取驱动树失败，型号地址能力未知，退回默认地址兜底', { error: String(error) })
    }
  }

  return { driverTree, driverTreeLoaded, fetchDriverTreeOnce, resolveModelRequiresTargetAddress }
}
