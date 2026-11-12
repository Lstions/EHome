/**
 * 后端 envelope 解包（原 DataPanel.vue 内的 unwrapList，原样搬出）。
 *
 * 为什么必须只有一个实现：/api/v1/unified-data/categories、historical-batch、
 * historical 三个端点**同时存在两种响应形态** —— { code, data, message } envelope
 * 与裸数组（DataPanel.spec.ts 的两条「envelope / 裸数组」契约用例把它们钉死）。
 * 解包规则一旦在页面、趋势图、多设备对比三条取数链里各写一份，漏掉 envelope 的
 * 那一条会静默走空数组路径（图表恒空、对比恒"没有可安全比较的同单位指标"）。
 *
 * 数组统一从 data 取，其余形态一律空数组（fail-closed，不抛异常）。
 */
export function unwrapList<T>(res: unknown): T[] {
  if (Array.isArray(res)) return res as T[]
  if (res && typeof res === 'object' && Array.isArray((res as { data?: unknown }).data)) {
    return (res as { data: T[] }).data
  }
  return []
}
