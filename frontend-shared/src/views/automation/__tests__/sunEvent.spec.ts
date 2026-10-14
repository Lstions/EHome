import { describe, expect, it } from 'vitest'
import {
  SUN_KIND_OPTIONS, SUN_OFFSET_MAX, SUN_OFFSET_MIN,
  sunKindLabel, sunOffsetText, sunLocationState, sunLocationText,
} from '../sunEvent'

/**
 * F-1 门禁：日出/日落选项 + 偏移分钟（含**负值**）。
 *
 * 反向验证（方案 §8 F-1）：删除 sun_event 相关的 kind 选项或把偏移符号取反 →
 * 本文件必须变红。断言的是**函数的返回值**而不是源码字符串包含 —— 后者在
 * "函数存在但从不被调用"时同样会绿（祖父化）。
 */
describe('F-1 日出/日落触发器', () => {
  it('日出/日落是两个可选值，且文案与后端枚举同域', () => {
    expect(SUN_KIND_OPTIONS.map(o => o.value)).toEqual(['sunrise', 'sunset'])
    expect(SUN_KIND_OPTIONS.map(o => o.label)).toEqual(['日出', '日落'])
    expect(sunKindLabel('sunrise')).toBe('日出')
    expect(sunKindLabel('sunset')).toBe('日落')
  })

  it('偏移分钟实时回显：负值 = 之前，正值 = 之后，0 = 正点', () => {
    // 用户原始需求的原话就是"日落前 30 分钟" —— 界面必须直接说出这句话。
    expect(sunOffsetText('sunset', -30)).toBe('日落前 30 分钟')
    expect(sunOffsetText('sunrise', -30)).toBe('日出前 30 分钟')
    expect(sunOffsetText('sunset', 15)).toBe('日落后 15 分钟')
    expect(sunOffsetText('sunrise', 0)).toBe('日出正点')
  })

  it('偏移符号不得取反（反向验证的靶点）', () => {
    // 把实现改成 `offset < 0 ? '后' : '前'` 时本断言必红。
    expect(sunOffsetText('sunset', -1)).toContain('前')
    expect(sunOffsetText('sunset', 1)).toContain('后')
  })

  it('缺省/非法偏移按 0 处理，不产生 "前 NaN 分钟"', () => {
    expect(sunOffsetText('sunset', undefined)).toBe('日落正点')
    expect(sunOffsetText('sunset', null)).toBe('日落正点')
    expect(sunOffsetText('sunset', Number.NaN)).toBe('日落正点')
  })

  it('取值域是后端契约的 [-720, 720]', () => {
    expect(SUN_OFFSET_MIN).toBe(-720)
    expect(SUN_OFFSET_MAX).toBe(720)
  })
})

/**
 * F-1 的坐标/时区继承状态（用户裁决 O-3: 节点级权威 + 规则级可空覆盖）。
 *
 * 反向验证：把 filled() 改成真值判断（`if (!value) return false`）→ "纬度 0" 会被
 * 判成未填 → 本用例红。这正是后端 sun_location.go 用指针而非零值的原因
 * （0 是几内亚湾的合法坐标）。
 */
describe('F-1 坐标/时区继承状态', () => {
  it('三项全空 = 继承节点', () => {
    expect(sunLocationState({})).toBe('inherit')
    expect(sunLocationState({ latitude: null, longitude: null, timezone: '' })).toBe('inherit')
  })

  it('三项全填 = 规则级覆盖', () => {
    expect(sunLocationState({ latitude: 31.23, longitude: 121.47, timezone: 'Asia/Shanghai' })).toBe('override')
  })

  it('纬度 0 是合法坐标，不得被当成"未填"', () => {
    expect(sunLocationState({ latitude: 0, longitude: 0, timezone: 'UTC' })).toBe('override')
  })

  it('部分填写 = partial（后端 V2 必拒，前端提前拦）', () => {
    expect(sunLocationState({ latitude: 31.23 })).toBe('partial')
    expect(sunLocationState({ latitude: 31.23, longitude: 121.47 })).toBe('partial')
  })

  it('继承状态文案说明了 fail-closed 后果，不留白', () => {
    expect(sunLocationText({})).toContain('继承节点')
    expect(sunLocationText({})).toContain('拒绝保存')
    expect(sunLocationText({ latitude: 1 })).toContain('必须同时填写')
    expect(sunLocationText({ latitude: 1, longitude: 2, timezone: 'UTC' })).toContain('不继承节点')
  })
})
