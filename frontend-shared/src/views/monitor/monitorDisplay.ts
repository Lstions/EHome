import { UNKNOWN, metricOrDash } from '@/utils/format'

/**
 * 监控页「展示口径」纯函数（原 Monitor.vue 的 formatNumber / metric / metricText /
 * 在线数配色 / 统计卡范围词常量整段搬出，函数体一字未改；唯一的形式变化是把
 * 「指标是否可信」由闭包里的 metricsReady 改成显式入参 ready）。
 *
 * 边界依据（为什么单独抽这一层）：
 *   · 拆分后共有三处渲染同一批数字：页面的四个 KPI、控制面健康卡、详细面板六卡。
 *     scoped 样式与 props 都解决不了「未知值怎么显示」——它必须是同一份判据，
 *     否则失败态会出现「页面显示 —、卡片显示 0」的口径分裂（缺陷 F28 / U-1 家族）；
 *   · 入参只有「值 + 是否可信 + 语义标注」，出参是字符串，无状态、无请求、无副作用，
 *     搬动它不改变任何调用时序；
 *   · 未知态的唯一落点仍是 @/utils/format 的 UNKNOWN / metricOrDash：本模块保留
 *     「import { UNKNOWN, metricOrDash } from '@/utils/format'」这一行原文 ——
 *     Monitor.spec 的源码门禁逐字断言该行存在（拆分后按「页面 + 拆分产物」拼接读取，
 *     见 __tests__/Monitor.spec.ts 文件头），把两者拆到不同文件会让判据失去对象。
 */

/**
 * 统计卡范围词（规范 §4.3 统计卡 MUST）。
 * 常量化的理由：验收要能机械地证明「每个统计值都带了范围词」，
 * 字面量散落则无法审计，也无法与 /data 的范围词保持一致。
 *
 * 2026-11-12 纯搬移：范围词现在同时被「页面 4 个 KPI」与「详细面板的数据采集卡」使用，
 * 故从页面脚本提到本模块具名导出 —— 两处各存一份字面量必然漂移。
 */
export const SCOPE_PROCESS = '进程启动以来'
export const SCOPE_GLOBAL = '全局'

/** 大数缩写（K/M）。仅接受数字：未知态在进入本函数前已被 metricOf 拦下。 */
export const formatNumber = (num: number): string => {
  if (num >= 1000000) {
    return (num / 1000000).toFixed(2) + 'M'
  }
  if (num >= 1000) {
    return (num / 1000).toFixed(2) + 'K'
  }
  return num.toString()
}

/**
 * 指标取值：未知一律显示 UNKNOWN「—」，绝不回落成 0。
 * 注意计数器（Counter）与仪表（Gauge）在**显示层**同构 —— 缺字段都是「接口没说」，
 * 故 isCounter 只作语义标注参数保留，不改变返回值（与 views/data-source 的 metric() 一致）。
 */
export const metricOf = (
  value: number | null | undefined,
  ready: boolean,
  isCounter = false,
): string | number => {
  void isCounter
  return metricOrDash(value, ready)
}

/** 可格式化取数：未知时返回 UNKNOWN 占位符，交由 formatNumber 原样透出。 */
export const metricTextOf = (
  value: number | null | undefined,
  ready: boolean,
  isCounter = false,
): string | number => {
  const v = metricOf(value, ready, isCounter)
  return typeof v === 'number' ? formatNumber(v) : v
}

/**
 * 展示用的分母（设备总数 / 节点总数）进入未知态。
 *
 * 派生值本身保留 0（数组/计数求和的空集就是 0），但**渲染**时必须跟分子一起进入未知态
 * —— 否则失败态会渲染出「— / 0」，那个 0 又会被读成「总共有 0 台设备」，
 * 正是本缺陷的同一类伪装。
 */
export const readyOrUnknown = (ready: boolean, value: number): number | typeof UNKNOWN =>
  ready ? value : UNKNOWN

// 在线数为 0 且总数 > 0 时用告警色，避免与同页「离线=红」的语义冲突
export const onlineValueClass = (online: number | undefined, total: number) => {
  return total > 0 && (online || 0) === 0 ? 'online-none' : 'online'
}
