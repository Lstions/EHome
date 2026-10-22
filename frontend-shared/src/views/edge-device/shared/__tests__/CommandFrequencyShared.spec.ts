import { describe, expect, it } from 'vitest'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import sharedSource from '@/views/edge-device/shared/CommandFrequencySection.vue?raw'
import bmsDetailSource from '@/views/edge-device/bms/BmsDetailPage.vue?raw'
import inverterSource from '@/views/edge-device/inverter/InverterDetailPage.vue?raw'
import genericSource from '@/views/edge-device/GenericDeviceDetail.vue?raw'

/**
 * 指令频率 / 轮询指令配置「单一实现」契约（2026-10-22 第七轮）。
 *
 * 用户原话：「指令频率应该抽一个公共组件出来，大家一起通用」。
 * 改前是三页两套实现：
 *   · BMS 详情页自建 .poll-row + 自己的请求/保存/容量门（约 500 行）；
 *   · 逆变器/通用设备页走 CommandFrequencySection + CommandList（另有 470 行）。
 * 两份实现从此各自漂移，最终连「窄容器不覆盖文字」这种基础布局都同时出问题。
 *
 * 本文件锁三件事：
 *   1. 结构：全站只有 CommandFrequencySection 一个实现，CommandList.vue 不得复活；
 *   2. 接入：BMS / 逆变器 / 通用设备三页都必须用它（BMS 还要带「轮询指令配置」标题与容量门语义）；
 *   3. 布局：容器感知的防重叠写法必须在场（任何人 rebase 回 grid 首列可被压成 0 的旧写法即红）。
 */
const SRC_ROOT = join(dirname(fileURLToPath(import.meta.url)), '../../../../../src')

describe('指令频率：全站唯一实现（诉求③「抽公共组件，大家一起通用」）', () => {
  it('旧的分裂实现 components/device/CommandList.vue 已删除且不得复活', () => {
    expect(existsSync(join(SRC_ROOT, 'components/device/CommandList.vue'))).toBe(false)
    const sources: Array<[string, string]> = [
      ['BmsDetailPage', bmsDetailSource],
      ['InverterDetailPage', inverterSource],
      ['GenericDeviceDetail', genericSource],
    ]
    for (const [name, src] of sources) {
      expect(src, name + ' 不得再引用已删除的 CommandList').not.toMatch(/components\/device\/CommandList/)
    }
  })

  it('三个设备页都接同一个共享组件；BMS 用它承载「轮询指令配置」与节点上报上限', () => {
    for (const [name, src] of [
      ['BMS', bmsDetailSource],
      ['逆变器', inverterSource],
      ['通用设备', genericSource],
    ] as Array<[string, string]>) {
      expect(src, name + ' 页必须使用 CommandFrequencySection').toContain('CommandFrequencySection')
      expect(src, name + ' 页必须传入设备 id').toMatch(/<CommandFrequencySection[\s\S]{0,200}:device-id=/)
    }
    // BMS 页的差异化只允许是「标题 + 说明 + 节点上限」这类 props，不允许再自带一套行实现
    expect(bmsDetailSource).toContain('title="轮询指令配置"')
    expect(bmsDetailSource).toContain(':max-polling-commands="device?.max_polling_commands"')
  })

  it('BMS 页的私有轮询实现已删除，不得以任何结构化形式复活', () => {
    for (const marker of [
      'class="poll-row"',
      '.poll-row {',
      '.poll-interval {',
      '.poll-controls {',
      'pollCommands',
      'isPollEnabled',
      'loadPollCommands',
      'savePoll',
      'note-limit-detail',
    ]) {
      expect(bmsDetailSource, 'BMS 页私有轮询实现复活：' + marker).not.toContain(marker)
    }
    // 分母守卫：BMS 页仍是详情页且轮询区块在场（负向断言不是对空文件成立）
    expect(bmsDetailSource).toContain('class="row stream-poll"')
    expect(bmsDetailSource).toContain('轮询指令配置')
  })

  it('页面不得为了容量提示重复请求同一接口（条数由组件外抛，避免两套事实源）', () => {
    expect(bmsDetailSource).toContain('@usage-change="onUsageChange"')
    expect(bmsDetailSource).not.toContain('getCommandIntervals')
    // 共享组件内部才是唯一请求点
    expect(sharedSource).toContain('edgeDeviceApi.getCommandIntervals')
  })
})

describe('指令频率：容器感知布局契约（修「内容被输入框覆盖遮挡」）', () => {
  /**
   * 根因（2026-10-22 真实页面实测，设备页 /edge-device/15，320px 宽）：
   *   ≤768px 的 grid 是 grid-template-columns: minmax(0,1fr) auto ——
   *   右列 auto 由「输入框 104 + ms + 开关」的 max-content（≈161px）决定，
   *   左列 1fr 的 min 是 0 ⇒ 被压到 19px；而 .cmd-name 是 word-break:keep-all
   *   且没有 overflow-wrap ⇒ 命令名画出格子，被后画的输入框压住。
   *   （11 行里 9 行重叠，DOM 重叠面积 659px²；用户截图即此形态。）
   * 修法三层必须同时在：
   *   ① 名称允许「实在放不下时」断行（keep-all 防逐字竖排 + overflow-wrap:anywhere 兜底）；
   *   ② 容器内容宽 < 262px 时整行降级单列（名称 / 控件 / 描述各一行）；
   *   ③ 控件自身可换行、输入框可收缩，任何宽度都不得横向溢出。
   */
  it('名称可断行：keep-all 之后必须有 overflow-wrap 兜底（否则必然越界绘制）', () => {
    expect(sharedSource).toMatch(/\.cmd-name \{[^}]*word-break:\s*keep-all/s)
    expect(sharedSource).toMatch(/\.cmd-name \{[^}]*overflow-wrap:\s*anywhere/s)
  })

  it('窄容器降级为单列（@container）：两列形态不得是**唯一**形态', () => {
    // 容器查询在场（与视口解耦：分屏/内嵌面板/页面缩放都能正确降级）
    expect(sharedSource).toContain('container-type: inline-size')
    expect(sharedSource).toMatch(/@container \(max-width: \d+px\)/)
    // 单列堆叠的区域声明必须在容器降级里
    expect(sharedSource).toMatch(/@container[\s\S]{0,400}grid-template-areas:\s*'name'\s*'ctl'\s*'desc'/)
    // 缺陷根因是「两列 + 名称不可断行」的组合；两者必须各自被拆掉一个：
    //   · 名称可断行（上一个用例）；
    //   · 窄容器退回单列（本用例）。
    // 因此这里只要求「降级存在」，不禁止两列形态本身（宽容器仍要两列才紧凑）。
    expect(sharedSource).toMatch(/grid-template-columns:\s*minmax\(0,\s*1fr\) auto/)
  })

  it('控件不得固定不收缩：输入框有 min-width 下限且允许收缩，控件容器可换行', () => {
    expect(sharedSource).toMatch(/\.cmd-controls \{[^}]*flex-wrap:\s*wrap/s)
    expect(sharedSource).toMatch(/\.interval-input \{[^}]*min-width:/s)
    // 禁止整行 nowrap（旧缺陷之一：390px 下 180+200 > 310 数学上必然裁剪）
    expect(sharedSource).not.toContain('flex-wrap: nowrap')
  })

  it('描述文字允许换行且不得把整行撑出容器（任一宽度都不横向溢出）', () => {
    expect(sharedSource).toMatch(/\.cmd-desc \{[^}]*overflow-wrap:\s*anywhere/s)
  })
})

describe('指令频率：语义契约（两套实现合并后不得丢行为）', () => {
  it('保存后确认节点已应用（来自共享实现）：状态三档且不谎报', () => {
    expect(sharedSource).toContain('已保存，正在同步到节点…')
    expect(sharedSource).toContain('已同步到节点')
    expect(sharedSource).toContain('已保存')
  })

  it('容量门（来自 BMS 实现）：超限点名 + 逐行「超限」标签 + 一键禁用 + 保存判死', () => {
    expect(sharedSource).toContain('超过上限')
    expect(sharedSource).toContain('excessNames')
    expect(sharedSource).toContain('超限')
    // 超限时保存按钮必须禁用（后端会整单 400 拒绝且不落库）
    expect(sharedSource).toMatch(/:disabled="!dirty \|\| overLimit"/)
  })

  it('未上报上限时不假装有上限（不显示分母 / 不显示容量提示）', () => {
    expect(sharedSource).toContain('limitValue > 0')
    expect(sharedSource).not.toContain('最多 {{ maxPollingCommands }}')
  })

  it('指令标识：ASCII 协议显示助记符，不显示假 0x00', () => {
    expect(sharedSource).toContain('commandToken')
    expect(sharedSource).not.toContain("cmd_byte === 0 ? '0x00'")
  })
})

/** 反向保险：src 里除本组件外，不得再出现「自建轮询行」类 CSS 契约（防第三份实现） */
describe('反向契约：全仓不得出现第三份轮询行实现', () => {
  function walk(dir: string, acc: string[] = []): string[] {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, entry.name)
      if (entry.isDirectory()) walk(p, acc)
      else if (entry.name.endsWith('.vue')) acc.push(p)
    }
    return acc
  }

  it('只有共享组件定义 .cmd-identity / .interval-input 这类轮询行样式', () => {
    const offenders = walk(join(SRC_ROOT, 'views'))
      .concat(walk(join(SRC_ROOT, 'components')))
      .filter((p) => !p.endsWith('CommandFrequencySection.vue'))
      .filter((p) => {
        const text = readFileSync(p, 'utf-8')
        return /\.cmd-identity\s*\{/.test(text) || /\.interval-input\s*\{/.test(text)
      })
    expect(offenders, '出现第二份轮询行实现：' + offenders.join(', ')).toEqual([])
  })
})
