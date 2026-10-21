import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
import bmsDetailSource from '@/views/edge-device/bms/BmsDetailPage.vue?raw'
import metricCardSource from '@/components/common/MetricStatCard.vue?raw'
import { REALTIME_FIRST_DATA_GRACE_MS, resolveRealtimeViewState } from '../realtimeViewState'

/**
 * BMS 详情页首屏加载态契约（2026-10-21，用户真机反馈「BMS 页面刚进去没数据应该展示加载状态」）。
 *
 * 缺陷（生产实测 0.5/1.5/3/6s 采样）：刚进入 /edge-device/14 时
 *   · 顶栏 4 张指标卡显示「—」（读起来像设备没有数据，实际是还在加载）；
 *   · 保护状态卡显示「暂无数据」并列出 12 个「正常」徽标（把「未知」伪装成「全部正常」，
 *     这是安全语义错误 —— 真故障会被当成正常）；
 *   · MOS 卡显示「尚无 fet_status 数据」，行内却给出「关断/导通」结论；
 * 而真实状态是**还在加载**（快照在途 / 设备还没推首帧）。错误与空态共用「暂无数据」，
 * 也会让「加载失败」看起来像「设备没数据」。
 *
 * 本 spec 锁两层：
 *   ① 纯函数四态（loading/error/empty/data）—— 加载中不得被判定成空态或错误态；
 *   ② 源码契约 —— 加载只出骨架、错误有独立文案 + 重试、保护位未知显示「未知」而非「正常」。
 */

describe('realtimeViewState 四态判定（加载中不得伪装成空数据）', () => {
  const base = { hasData: false, snapshotLoading: false, snapshotError: '', graceOver: false }

  it('首屏快照在途 → loading', () => {
    expect(resolveRealtimeViewState({ ...base, snapshotLoading: true })).toBe('loading')
  })

  it('快照已返回但首帧宽限期未过（设备是周期上报）→ 仍是 loading，不能提前说「暂无数据」', () => {
    expect(resolveRealtimeViewState({ ...base, graceOver: false })).toBe('loading')
    // 宽限期必须覆盖用户的 6s 采样窗口
    expect(REALTIME_FIRST_DATA_GRACE_MS).toBeGreaterThanOrEqual(6000)
  })

  it('请求失败且没有任何数据 → error（独立错误态，不是空态）', () => {
    expect(resolveRealtimeViewState({ ...base, snapshotError: 'timeout of 10000ms exceeded' })).toBe('error')
  })

  it('请求失败后 WS 又送来数据 → data（失败可自愈，不被错误态锁死）', () => {
    expect(resolveRealtimeViewState({ ...base, hasData: true, snapshotError: 'timeout' })).toBe('data')
  })

  it('宽限期已过 + 请求成功但设备确实没上报 → empty（明确的空态）', () => {
    expect(resolveRealtimeViewState({ ...base, graceOver: true })).toBe('empty')
  })
})

describe('MetricStatCard loading：骨架而不是把「—」当值', () => {
  it('loading=true 只渲染骨架：不渲染 value/unit/辅助槽，也不出现「—」', () => {
    const wrapper = mount(MetricStatCard, {
      props: { label: 'SOC 剩余电量', value: '—', unit: '%', subText: '健康区间 20% – 90%', loading: true },
    })
    expect(wrapper.find('.metric-skeleton').exists()).toBe(true)
    expect(wrapper.find('.metric-value').exists()).toBe(false)
    expect(wrapper.find('.metric-label').exists()).toBe(false)
    expect(wrapper.find('.metric-unit').exists()).toBe(false)
    expect(wrapper.find('.metric-sub').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('—')
    // 读屏可感知「加载中」，卡片标记 aria-busy
    expect(wrapper.text()).toContain('正在加载')
    expect(wrapper.get('.metric-card').attributes('aria-busy')).toBe('true')
  })

  it('loading=false（默认）行为不变：渲染真实数值，无骨架、无 aria-busy', () => {
    const wrapper = mount(MetricStatCard, { props: { label: '电流', value: '1.92', unit: 'A' } })
    expect(wrapper.get('.metric-value').text()).toContain('1.92')
    expect(wrapper.find('.metric-skeleton').exists()).toBe(false)
    expect(wrapper.get('.metric-card').attributes('aria-busy')).toBeUndefined()
  })

  it('源码契约：loading 走 v-if 分支渲染骨架，数据分支在 v-else 内', () => {
    expect(metricCardSource).toContain(':aria-busy="loading ? \'true\' : undefined"')
    expect(metricCardSource).toMatch(/<template v-if="loading">[\s\S]{0,400}?metric-skeleton/)
    expect(metricCardSource).toMatch(/<template v-else>[\s\S]{0,400}?metric-value/)
  })
})

describe('BmsDetailPage 首屏：四态接入 + 三块卡片加载/错误态（源码契约）', () => {
  it('四态来自纯函数，首帧宽限期在挂载/切设备/重试三处都会重新计时', () => {
    expect(bmsDetailSource).toContain('resolveRealtimeViewState({')
    expect(bmsDetailSource).toContain('REALTIME_FIRST_DATA_GRACE_MS')
    expect(bmsDetailSource).toContain('snapshotLoading')
    expect((bmsDetailSource.match(/startFirstDataGrace\(\)/g) || []).length).toBeGreaterThanOrEqual(3)
  })

  it('4 张指标卡全部传 :loading="realtimeLoading"（首屏骨架，不显示「—」当值）', () => {
    const count = (bmsDetailSource.match(/:loading="realtimeLoading"/g) || []).length
    expect(count, '4 张指标卡必须全部接入 loading').toBe(4)
  })

  it('保护状态：加载中显示「正在加载保护状态…」骨架；12 项徽标只在非加载分支渲染', () => {
    const cardStart = bmsDetailSource.indexOf('class="card protect-card"')
    expect(cardStart).toBeGreaterThan(0)
    const cardEnd = bmsDetailSource.indexOf('12 位明细')
    const card = bmsDetailSource.slice(cardStart, cardEnd)
    // 骨架块（.realtime-state）里给出「正在加载保护状态…」
    expect(card).toMatch(/class="realtime-state"[\s\S]{0,300}?正在加载保护状态/)
    // 加载块内不得含任何 el-tag（防止「未知」被渲染成「正常」徽标）
    const skeletonIdx = card.indexOf('class="realtime-state"')
    const noteIdx = card.indexOf('realtime-state-note', skeletonIdx)
    expect(skeletonIdx).toBeGreaterThan(0)
    expect(noteIdx).toBeGreaterThan(skeletonIdx)
    const loadingBlock = card.slice(skeletonIdx, card.indexOf('</div>', noteIdx))
    expect(loadingBlock).toContain('el-skeleton')
    expect(loadingBlock).not.toContain('<el-tag')
    // 加载中不得渲染「暂无数据」「12 项正常」这类结论，也不得把「—」当值
    expect(loadingBlock).not.toContain('暂无')
    expect(loadingBlock).not.toContain('12 项保护均未触发')
    expect(loadingBlock).not.toContain('—')
    // 12 项网格必须排在加载/错误分支之后（只在数据/空态渲染）
    const gridIdx = bmsDetailSource.indexOf('class="protect-grid"')
    expect(gridIdx).toBeGreaterThan(bmsDetailSource.lastIndexOf('正在加载保护状态'))
    expect(gridIdx).toBeGreaterThan(bmsDetailSource.indexOf('保护状态加载失败'))
  })

  it('保护位未知时显示中性「未知」，禁止把「未知」渲染成绿色「正常」', () => {
    expect(bmsDetailSource).toContain(':type="p.active ? \'danger\' : hasProtectionData ? \'success\' : \'info\'"')
    expect(bmsDetailSource).toContain("{{ p.active ? '已触发' : (hasProtectionData ? '正常' : '未知') }}")
    // 反向守卫：旧写法（未知也渲染 success 绿「正常」）不得回归
    expect(bmsDetailSource).not.toContain(':type="p.active ? \'danger\' : \'success\'"')
    // 「已触发」计数也不得在未知时推断
    expect(bmsDetailSource).toContain('hasProtectionData.value ? protectionBits.value.filter((p) => p.active) : []')
  })

  it('MOS 卡片：加载中显示「正在加载 MOS 状态…」，未知不得说成「关断」', () => {
    const cardStart = bmsDetailSource.indexOf('class="card mos-card"')
    const cardEnd = bmsDetailSource.indexOf('class="card protect-card"')
    const card = bmsDetailSource.slice(cardStart, cardEnd)
    expect(card).toMatch(/class="realtime-state"[\s\S]{0,300}?正在加载 MOS 状态/)
    const skeletonIdx = card.indexOf('class="realtime-state"')
    const noteIdx = card.indexOf('realtime-state-note', skeletonIdx)
    expect(skeletonIdx).toBeGreaterThan(0)
    expect(noteIdx).toBeGreaterThan(skeletonIdx)
    const loadingBlock = card.slice(skeletonIdx, card.indexOf('</div>', noteIdx))
    expect(loadingBlock).toContain('el-skeleton')
    expect(loadingBlock).not.toContain('允许充电')
    expect(loadingBlock).not.toContain('禁止充电')
    expect(loadingBlock).not.toContain('尚无 fet_status 数据')
    expect(loadingBlock).not.toContain('暂无')
    // 未知状态标签「未知」，描述明确「不等于关断」
    expect(bmsDetailSource).toContain("{{ m.known ? (m.on ? '导通' : '关断') : '未知' }}")
    expect(bmsDetailSource).toContain('尚未收到 fet_status 字段（状态未知，不等于关断）')
  })

  it('错误态与空态文案分离：错误专属「加载失败」+ 重试；「暂无数据」只用于明确空态', () => {
    expect(bmsDetailSource).toContain('实时数据加载失败')
    expect(bmsDetailSource).toContain('MOS 状态加载失败')
    expect(bmsDetailSource).toContain('保护状态加载失败')
    expect(bmsDetailSource).toContain('设备暂未上报实时数据')
    const retries = (bmsDetailSource.match(/@click="retryRealtime"/g) || []).length
    expect(retries, '错误态必须提供重试入口（页面错误条 + MOS + 保护）').toBeGreaterThanOrEqual(3)
    // 「—」的语义在错误/空态里被明确说明，不再假装是设备值
    expect(bmsDetailSource).toContain('下方数字显示「—」表示未知')
    // 空态提示只由 realtimeEmpty（宽限期过且确实没数据）驱动，loading 不得复用它
    expect(bmsDetailSource).toMatch(/v-else-if="realtimeEmpty"[\s\S]{0,300}?设备暂未上报实时数据/)
  })

  it('同一数据源的电芯电压/温度/实时流在加载中也不再写「暂无数据」', () => {
    expect(bmsDetailSource).toContain("realtimeLoading ? '正在加载单体电压…'")
    expect(bmsDetailSource).toContain("realtimeLoading ? '正在加载温度数据…'")
    expect(bmsDetailSource).toContain("realtimeLoading ? '正在等待设备实时数据…'")
    // 真·空态文案保留（fail-open：确实没数据时仍明确告知）
    expect(bmsDetailSource).toContain('暂无单体电压数据')
    expect(bmsDetailSource).toContain('暂无温度数据')
  })
})
