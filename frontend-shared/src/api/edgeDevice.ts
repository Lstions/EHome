/**
 * 边缘设备 API —— 统一出口（barrel）。
 *
 * 按接口域拆分后的**唯一对外入口**：本文件只做「合并 + 全量转出」，不含任何请求逻辑，
 * 因此 `import { edgeDeviceApi } from '@/api/edgeDevice'`、`import { compactEdgeDeviceList }`
 * 以及全部 `import type { ... } from '@/api/edgeDevice'` 的既有写法逐字不变。
 *
 * 域边界依据：
 *   · list     —— 在役设备的集合视图与增删改（/edge-devices、batch-delete、batch-update）
 *   · detail   —— 单设备详情 + 跨命令聚合的最新值（/:id、/:id/latest-values）
 *   · deleted  —— 已软删设备：列表 / 恢复 / 彻底删除（/deleted、/:id/restore）
 *   · history  —— 历史采样与历史数据管理（/devices/:id/history、data-summary、merge-history）
 *   · commands —— 驱动指令模板与逐指令轮询间隔（/drivers/:type/commands、/:id/commands）
 *
 * 归一化（normalize / compactEdgeDeviceList 等）单列在 ./edgeDevice/normalize：
 * 列表、详情、恢复三条读取路径共用它，塞进任一域都会制造反向依赖。
 */
import { listApi } from './edgeDevice/list'
import { detailApi } from './edgeDevice/detail'
import { deletedApi } from './edgeDevice/deleted'
import { historyApi } from './edgeDevice/history'
import { commandsApi } from './edgeDevice/commands'

export const edgeDeviceApi = {
  ...listApi,
  ...detailApi,
  ...deletedApi,
  ...historyApi,
  ...commandsApi,
}

export * from './edgeDevice/types'
export { compactEdgeDeviceList } from './edgeDevice/normalize'
