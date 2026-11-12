import { computed, nextTick, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Cloudy, Document, Grid, House, Monitor, Share, Tools, UserFilled } from '@element-plus/icons-vue'

/**
 * 页签栏的**导航与可见性**：页签名册、?tab= 深链消费、以及把激活页签滚入视野。
 *
 * 为什么这部分要独立成 composable：它只依赖路由 query 与自己的 DOM 引用，
 * 与节点数据/总线资源无关；留在页面里会和页头动作、弹窗状态搅在一起，
 * 而它恰恰是移动端最容易回归的一块（深链激活项不可见）。
 *
 * 注意：页签切换**触发哪些补拉**不在这里（activateTab 留在页面），
 * 因为那属于页面级编排 —— 它要同时指挥总线能力、DMA、关联设备、OTA 四份数据。
 */
export function useNodeTabs() {
  const route = useRoute()

  const tabs = [
    { label: '基本信息', icon: House },
    { label: '总线配置', icon: Share },
    { label: '外设控制', icon: Tools },
    { label: 'DMA 通道', icon: Grid },
    { label: '关联设备', icon: UserFilled },
    { label: 'OTA 历史', icon: Cloudy },
    { label: '系统日志', icon: Document },
    { label: '通道终端', icon: Monitor },
  ]
  const activeTab = ref('基本信息')

  /**
   * 消费列表页快捷入口的 `?tab=` 深链（NodeList.vue 的「配置」按钮推 `?tab=config`）。
   *
   * 改前该 query 全仓无消费者 —— 用户从节点列表点「配置」只是原地换了个 URL，
   * 落在「基本信息」TAB，看起来像点了没反应。映射表把外部的短名收敛到本页 TAB 标签，
   * 未知值一律忽略（不报错、不白屏），保持「基本信息」默认。
   */
  const TAB_QUERY_MAP: Record<string, string> = {
    config: '总线配置',
    bus: '总线配置',
    peripheral: '外设控制',
    gpio: '外设控制',
    pwm: '外设控制',
    dma: 'DMA 通道',
    devices: '关联设备',
    ota: 'OTA 历史',
    logs: '系统日志',
    terminal: '通道终端',
    info: '基本信息',
  }
  function applyTabQuery(value: unknown) {
    if (typeof value !== 'string') return
    const target = TAB_QUERY_MAP[value]
    if (target) activeTab.value = target
  }
  applyTabQuery(route.query.tab)
  // 同一路由内 query 变化（如在页内再次点「配置」）也要生效，故 watch 而非仅初始化。
  watch(() => route.query.tab, applyTabQuery)

  /**
   * M1：把**激活的**页签滚入视野。
   *
   * 390px 实测缺陷：`?tab=terminal` 深链进来后激活标签「通道终端」在 776–861px，
   * 而 .tab-bar 可视区右边界只有 362px、`scrollLeft` 仍是 0 ⇒ 手机上栏里只显示
   * 「基本信息/总线配置/外设控制」，**用户看不到自己在哪个页签**。
   *
   * 为什么必须 nextTick + 元素查询（时序）：
   *  · 标签栏在 `<template v-else-if="node">` 里，而 `node` 由 fetchDetail() **异步**填充
   *    —— 首屏那一刻 DOM 里根本没有 `.tab-item`，此刻 scrollIntoView 会拿到 null 而静默无效；
   *  · 深链场景下 `applyTabQuery` 在 setup 阶段就改好了 activeTab，早于首次渲染，
   *    所以「watch activeTab」的即时回调同样查不到元素。
   *  故同时观察 `tabBarRef`：它在元素**真正挂载**时才由 null 变成节点，
   *  那正是"标签栏已存在"的唯一可靠信号；再排到 nextTick 确保 activeTab 的新样式
   *  已落到 DOM。查不到就安静返回 —— 后续 activeTab/挂载变化仍会再次触发，不会永久失效。
   *
   * 为什么不用 `scrollLeft = 0` 之类的"重置"：那会让每次切换页签都跳回栏首，
   * 既没解决"激活项在右侧看不见"，又新增了跳动。`inline:'nearest'` 只在必要时滚动。
   * `block:'nearest'` 不可省：省略时浏览器按默认 `block:'start'` 处理，会把标签栏
   * 滚到视口顶部，导致页面纵向跳动（移动端尤其明显）。
   */
  const tabBarRef = ref<HTMLElement | null>(null)

  /**
   * A-nodes P2（2026-10-21）：393px 下 8 个页签只有前 3 个完整可见（第 4 个仅露 18px 图标），
   * 其余 4 个完全不可见，而 .tab-bar 虽可横滑却没有任何指示（无滚动条、无渐隐）。
   * 不改结构，只把滚动状态挂成类，窄屏用**两侧渐隐**（mask-image）表达"还有内容"：
   *   · tab-overflow：内容确实溢出（桌面 1192/1192 不溢出 ⇒ 不加类 ⇒ 桌面零变化）；
   *   · tab-at-start / tab-at-end：滚动到哪一端 —— 只对**还有内容的一侧**渐隐，
   *     已经在最右时右侧不再变暗（避免"永远像被裁掉"的假信号）。
   */
  const tabOverflow = ref(false)
  const tabAtStart = ref(true)
  const tabAtEnd = ref(true)
  function updateTabScrollState() {
    const bar = tabBarRef.value
    if (!bar) return
    const max = bar.scrollWidth - bar.clientWidth
    tabOverflow.value = max > 1
    tabAtStart.value = bar.scrollLeft <= 1
    tabAtEnd.value = bar.scrollLeft >= max - 1
  }
  const tabBarScrollClass = computed(() => ({
    'tab-overflow': tabOverflow.value,
    'tab-at-start': tabAtStart.value,
    'tab-at-end': tabAtEnd.value,
  }))

  async function scrollActiveTabIntoView() {
    await nextTick()
    const bar = tabBarRef.value
    if (!bar) return
    // 用 data-tab 逐项比对定位，不靠文本匹配、也不用 CSS.escape 拼选择器：
    // 文本匹配会因"包含关系"选错元素，而 CSS.escape 在 happy-dom(单测环境)下不存在。
    const active = Array.from(bar.querySelectorAll<HTMLElement>('.tab-item'))
      .find(el => el.dataset.tab === activeTab.value)
    if (!active) return
    active.scrollIntoView({ inline: 'nearest', block: 'nearest' })
    // scrollIntoView 的滚动位置在下一帧才稳定，故延后读 scrollLeft 刷新渐隐状态
    requestAnimationFrame(() => { updateTabScrollState() })
  }
  // flush:'post' 保证在 DOM 更新后执行；同时观察 tabBarRef 覆盖"异步数据到达后标签栏才挂载"。
  watch([activeTab, tabBarRef], () => { void scrollActiveTabIntoView() }, { immediate: true, flush: 'post' })

  /**
   * 模板引用用函数式绑定：ref 对象在模板里会被自动解包成当前值，
   * 写成 :ref="tabBarRef" 反而传不进 ref 本身。函数形式既保住了
   * 「组件卸载时置 null」的语义，也让 ref 只在本模块内被读写。
   */
  function setTabBarRef(el: unknown) {
    tabBarRef.value = (el as HTMLElement | null) ?? null
  }

  return {
    tabs,
    activeTab,
    setTabBarRef,
    tabBarScrollClass,
    updateTabScrollState,
  }
}
