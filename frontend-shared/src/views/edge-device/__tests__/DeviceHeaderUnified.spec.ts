import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * 边缘设备页头统一契约（2026-10-21 用户要求）。
 *
 * 用户原话两条：
 *   ①「边缘设备的顶部头没有统一，应该使用统一的组件」
 *   ②「边缘设备中不应该再增加面包屑，要使用最上方框架的面包屑进行返回或跳转上层」
 *
 * ①的失败模式很隐蔽：三个页面**都**用了 DeviceHeader 组件，但传的 props 不同 ——
 *   BMS 传 tags+subtitle（3 行信息），逆变器/通用只传 title（1 行）。
 *   组件相同、内容不同 ⇒ 视觉上仍是「没统一」。所以契约必须锁「传了哪些维度」，
 *   而不只是锁「用了哪个组件」。
 *
 * ②的失败模式：详情页自建一套「边缘设备管理 / BMS 设备详情」，与顶栏并存，
 *   同一屏两条面包屑，且「返回列表」被从顶栏剥离。
 */
const read = (p: string) => readFileSync(resolve(process.cwd(), p), 'utf8')

const BMS = read('src/views/edge-device/bms/BmsDetailPage.vue')
const INVERTER = read('src/views/edge-device/inverter/InverterDetailPage.vue')
const GENERIC = read('src/views/edge-device/GenericDeviceDetail.vue')
const HEADER = read('src/views/edge-device/shared/DeviceHeader.vue')
const LAYOUT = read('src/views/layout/MainLayout.vue')
const NODE_OVERVIEW = read('src/views/node/NodeOverview.vue')

describe('三设备页页头必须同维度（不只是同一个组件）', () => {
  const pages = [
    ['BMS', BMS],
    ['逆变器', INVERTER],
    ['通用设备', GENERIC],
  ] as const

  it('三页都使用 DeviceHeader 组件', () => {
    for (const [name, src] of pages) {
      expect(src, name + ' 未使用 DeviceHeader').toContain('<DeviceHeader')
    }
  })

  it('三页都传 tags（状态 + 类型标签）', () => {
    for (const [name, src] of pages) {
      expect(src, name + ' 未传 :tags').toContain(':tags="headerTags"')
    }
  })

  it('三页都传 subtitle（节点 · 通道 · 设备号 · 固件）', () => {
    for (const [name, src] of pages) {
      const hasSubtitle = src.includes(':subtitle="headerSubtitle"') || src.includes('#subtitle')
      expect(hasSubtitle, name + ' 未传副标题').toBe(true)
    }
  })

  it('三页都接同一组事件（back / sync / refresh / edit 或 updated / delete 或 update）', () => {
    for (const [name, src] of pages) {
      expect(src, name + ' 缺 @back').toMatch(/@back=/)
      expect(src, name + ' 缺 @sync-to-h-a').toMatch(/@sync-to-h-a=/)
      expect(src, name + ' 缺 @refresh').toMatch(/@refresh=/)
    }
  })
})

describe('详情页不得自建面包屑（统一由顶部框架承载）', () => {
  it('DeviceHeader 组件内已无任何面包屑 DOM / prop', () => {
    expect(HEADER).not.toContain('dh-crumb')
    expect(HEADER).not.toContain('breadcrumb?:')
    expect(HEADER).not.toContain('goCrumb')
  })

  it('三个设备页与节点总览都不再传 breadcrumb / 自带面包屑', () => {
    for (const [name, src] of [['BMS', BMS], ['逆变器', INVERTER], ['通用设备', GENERIC], ['节点总览', NODE_OVERVIEW]] as const) {
      expect(src, name + ' 仍在传 :breadcrumb').not.toContain(':breadcrumb=')
      expect(src, name + ' 仍有页内面包屑类').not.toMatch(/class="(no-breadcrumb|dh-crumb)"/)
    }
  })
})

describe('顶部框架面包屑承担层级导航', () => {
  it('按路径分段生成，每级都有可点目标', () => {
    expect(LAYOUT).toContain('const segments = route.path.split')
    expect(LAYOUT).toMatch(/crumbs\.push\(\{ text: label, to: acc \}\)/)
  })

  it('末项不可点（当前页不该是可返回链接）', () => {
    expect(LAYOUT).toMatch(/:to="index < breadcrumbs.length - 1 \? \{ path: item\.to \} : undefined"/)
  })

  it('三级路径不会退化成重复文案（/node/3/overview 曾是「节点详情 / 节点详情」）', () => {
    expect(LAYOUT).toContain('BREADCRUMB_SUB_LABELS')
    expect(LAYOUT).toMatch(/i === 1[\s\S]{0,120}BREADCRUMB_DETAIL_LABELS/)
  })
})