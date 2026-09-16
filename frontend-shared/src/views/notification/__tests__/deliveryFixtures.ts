import { defineComponent, h, inject, provide } from 'vue'
import type { NotificationChannel, NotificationDelivery } from '@/api/notificationChannel'

/**
 * 通知域两个列表页 spec 共用的夹具（**复用而不是复制**）。
 *
 * 为什么抽出来：NotificationChannels.spec.ts 里的 el-table 替身（provide/inject 版，
 * 能渲染列的行作用域插槽）与通道工厂 makeChannel 是**逐字可复用**的 ——
 * 新页 NotificationDeliveries.spec.ts 同样要点到行内内容、同样要造通道对象。
 * 复制一份的代价不是多几行，而是"下次改 EP 表格行为时要改两处、漏一处就有一个 spec 静默失真"。
 *
 * 注意：test-setup.ts 的通用 el-table stub **不渲染列的行作用域插槽**（它只把整行值串成一格），
 * 因此行内按钮/状态标签取不到。这里的替身让表 provide 行数据、列按行渲染自己的 scoped slot。
 */
export const ROWS = Symbol('rows')

export const ElTableStub = defineComponent({
  name: 'ElTable',
  props: { data: { type: Array, default: () => [] } },
  setup(props, { slots }) {
    provide(ROWS, props)
    return () =>
      h(
        'table',
        {
          class: 'el-table',
          // 把"页面交给 el-table 的行数"暴露成属性：本替身无法产出 <tbody><tr>
          // （列是子组件、按整表渲染，见 ElTableColumnStub 的说明），
          // 而「后端返回 N 行就必须渲染 N 行、不得本地裁剪」是要断言的语义。
          // 直接断言 data 长度比数 <tr> 更贴近契约，也不会因替身实现变化而失真。
          'data-row-count': String(props.data.length),
        },
        slots.default?.(),
      )
  },
})

export const ElTableColumnStub = defineComponent({
  name: 'ElTableColumn',
  props: {
    label: String,
    prop: String,
    width: [String, Number],
    minWidth: [String, Number],
    fixed: [String, Boolean],
  },
  setup(props, { slots }) {
    const table = inject<{ data: unknown[] } | null>(ROWS, null)
    return () => {
      const rows = table?.data ?? []
      // 有插槽 → 按行渲染插槽；无插槽（纯 prop 列，如 <el-table-column prop="id" />）
      // → 渲染该行的 prop 值。真实 el-table 正是这个语义；若退回成"渲染 label 文本"，
      // 断言行内容时就会看到列名而不是数据，掩盖"行其实没渲染出来"这类缺陷。
      const cell = slots.default
        ? rows.map((row, index) => slots.default!({ row, $index: index }))
        : rows.map((row) => h('span', String((row as Record<string, unknown>)?.[props.prop ?? ''] ?? '')))
      return h('td', { class: 'el-table-column', 'data-label': props.label }, cell)
    }
  },
})

export function makeChannel(overrides: Partial<NotificationChannel> = {}): NotificationChannel {
  return {
    id: 1,
    name: '运维群',
    type: 'wecom',
    target_url: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=***',
    secret_hint: 'abcd',
    has_secret: true,
    template: '',
    min_level: 'warning',
    enabled: true,
    timeout_sec: null,
    max_retries: 0,
    allow_private: false,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    ...overrides,
  }
}

export function makeDelivery(overrides: Partial<NotificationDelivery> = {}): NotificationDelivery {
  return {
    id: 91,
    notification_id: 3,
    channel_id: 1,
    state: 'delivered',
    attempt_no: 1,
    status_code: 200,
    error_message: '',
    duration_ms: 128,
    created_at: '2026-09-15T10:00:00Z',
    ...overrides,
  }
}
