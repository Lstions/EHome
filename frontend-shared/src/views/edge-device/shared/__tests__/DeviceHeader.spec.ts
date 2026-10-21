import { describe, expect, it } from 'vitest'
import source from '../DeviceHeader.vue?raw'

describe('DeviceHeader cache consistency', () => {
  it('keeps editable metadata cache-safe without exposing the retired address write', () => {
    expect(source).toContain('edgeDeviceStore.invalidateLists()')
    expect(source).toContain('edgeDeviceStore.invalidateDetail(deviceId)')
    expect(source).toContain('assertSessionGeneration(generation)')
    expect(source).toContain('props.device?.id !== deviceId')
    expect(source).not.toContain('changeAddress')
    expect(source).not.toContain('修改地址')
    expect(source).toContain('onUnmounted(() =>')
    expect(source).toContain('operationGeneration++')
  })

  it('deletes through the store so list, detail and in-flight caches are invalidated', () => {
    expect(source).toContain('edgeDeviceStore.deleteDevice(deviceId)')
    expect(source).not.toContain('edgeDeviceApi.delete(props.device.id)')
  })
})

/**
 * 统一设备页头契约（2026-10-21）：逆变器 / BMS / 通用设备三页共用同一组件。
 *
 * 用户诉求原文：「边缘设备顶部实时连接/刷新数据那里统一一个组件」。
 * 统一过程中的三个静默回退点，本文件逐条设防：
 *   ① 窄屏又挤回单行 → 标题折断、按钮掉行（真机实测 28px 高按钮）；
 *   ② ⋯ 菜单命令接丢 → 编辑/删除点了没反应（BMS 用 builtinDialogs=false 时尤其容易漏 emit）；
 *   ③ 面包屑可点段失去 UA 清零 → 老式按钮黑边（2026-10-20 视觉评审缺陷，断言从 BmsDetailPage 迁入）。
 */
describe('DeviceHeader 统一页头 API', () => {
  it('暴露三页共用的 props（身份 / 连接 / 刷新 / 操作可见性 / 面包屑 / 内置弹窗开关）', () => {
    for (const prop of [
      'device:', 'wsConnected?:', 'syncingHA?:', 'refreshing?:',
      'title?:', 'subtitle?:', 'tags?:', 'breadcrumb?:',
      'builtinDialogs?:', 'showEdit?:', 'showSync?:', 'showRefresh?:', 'showDelete?:',
    ]) {
      expect(source, '缺少 prop：' + prop).toContain(prop)
    }
  })

  it('事件契约齐全：back / syncToHA / refresh / edit / delete / updated / deleted', () => {
    for (const evt of [
      "(e: 'back'): void",
      "(e: 'syncToHA'): void",
      "(e: 'refresh'): void",
      "(e: 'edit'): void",
      "(e: 'delete'): void",
      "(e: 'updated'): void",
      "(e: 'deleted'): void",
    ]) {
      expect(source, '缺少事件：' + evt).toContain(evt)
    }
  })

  it('BMS 传 builtin-dialogs=false 时编辑/删除改为 emit 给父页；其它页仍用内置对话框', () => {
    // 分流必须成对存在：只写一半（比如忘记 emit）会让 BMS 的编辑/删除按钮点了没反应
    expect(source).toMatch(/function onEdit\(\) \{\s*if \(props\.builtinDialogs\) editDialogVisible\.value = true\s*else emit\('edit'\)/)
    expect(source).toMatch(/function onDelete\(\) \{\s*if \(props\.builtinDialogs\) deleteDialogVisible\.value = true\s*else emit\('delete'\)/)
    expect(source).toContain('v-if="builtinDialogs"')
    // 富文本副标题插槽（BMS 的节点/总线/设备号/固件/轮询数行）
    expect(source).toContain('<slot name="subtitle">')
  })
})

describe('DeviceHeader 窄屏确定性两行 + 桌面完整操作组', () => {
  it('窄屏第二行「刷新数据」独占整行；桌面才是完整操作组', () => {
    expect(source).toMatch(/<div v-if="!isMobile" class="dh-actions">/)
    expect(source).toMatch(/<div v-if="isMobile" class="dh-mobile-actions">/)
    expect(source).toMatch(/class="dh-refresh"/)
    // 刷新数据是窄屏保留的主操作，不得被收进 ⋯ 菜单
    expect(source.match(/<el-dropdown-item[^>]*command="refresh"/g)).toBeNull()
    expect(source).toContain('const { isMobile } = useResponsive()')
  })

  it('窄屏主操作固定 36px 档；粗指针升到 44px 触摸目标（不再被压成 28px）', () => {
    expect(source).toMatch(/\.dh-refresh \{[\s\S]{0,300}?height: 36px;/)
    expect(source).toMatch(/@media \(max-width: 768px\)[\s\S]*?\.dh-actions \{ display: none; \}/)
    expect(source).toMatch(/@media \(pointer: coarse\)[\s\S]*?\.dh-refresh \{ min-height: 44px; \}/)
  })

  it('设备名不按字符折断（keep-all），标签行可折行且不挤掉名称', () => {
    expect(source).toMatch(/\.dh-name \{[\s\S]{0,600}?word-break: keep-all;/)
    expect(source).toMatch(/\.dh-tags \{[\s\S]{0,120}?flex-wrap: wrap;/)
  })
})

describe('DeviceHeader 移动端 ⋯ 溢出菜单（编辑 / 同步到 HA / 删除）', () => {
  it('三个次级操作收进 dropdown，命令走同一组 handler', () => {
    // dropdown 在操作行（.dh-mobile-actions）内、不在身份行 —— 否则窄屏身份行被挤窄，标签/副文本多折一行
    expect(source).toMatch(/<el-dropdown v-if="showMoreMenu"[\s\S]*?popper-class="device-header-more-popover"/)
    const actStart = source.indexOf('<div v-if="isMobile" class="dh-mobile-actions">')
    expect(actStart).toBeGreaterThan(-1)
    const mobileActions = source.slice(actStart, source.indexOf('</div>', actStart))
    expect(mobileActions).toContain('device-header-more-popover')
    expect(mobileActions).toContain('刷新数据')
    expect(source).toMatch(/<el-dropdown-item v-if="showEdit" command="edit"[\s\S]*?编辑<\/el-dropdown-item>/)
    expect(source).toMatch(/<el-dropdown-item v-if="showSync" command="syncToHA"[\s\S]*?同步到 HA[\s\S]*?<\/el-dropdown-item>/)
    expect(source).toMatch(/<el-dropdown-item v-if="showDelete" command="delete"[\s\S]*?删除[\s\S]*?<\/el-dropdown-item>/)
    // 同一组 handler：编辑 / 删除再按 builtinDialogs 分流到内置弹窗或 emit 给父页
    expect(source).toContain('function handleMoreCommand(command: string)')
    expect(source).toMatch(/if \(command === 'edit'\) onEdit\(\)/)
    expect(source).toMatch(/else if \(command === 'syncToHA'\) emit\('syncToHA'\)/)
    expect(source).toMatch(/else if \(command === 'delete'\) onDelete\(\)/)
  })

  it('只读操作 flags 决定菜单有无；离线设备禁用同步；popper 限宽', () => {
    expect(source).toMatch(/const showMoreMenu = computed\(\(\) => props\.showEdit \|\| props\.showSync \|\| props\.showDelete\)/)
    expect(source).toMatch(/const canOperate = computed\(\(\) => \{[\s\S]*?status === 'online'/)
    expect(source).toContain(':disabled="!canOperate"')
    // el-dropdown 弹出层 teleport 到 body，scoped 样式不生效，必须 :global 限宽
    expect(source).toMatch(/:global\(\.device-header-more-popover\)\s*\{[^}]*max-width:\s*calc\(100vw - 24px\)[^}]*\}/)
  })
})

describe('DeviceHeader 面包屑（BMS 页使用）', () => {
  it('可点段是原生 button 且清空 UA 样式（2026-10-21 从 BmsDetailPage 迁入的断言）', () => {
    expect(source).toMatch(/<button v-if="item\.to" type="button" class="dh-crumb-link" @click="goCrumb\(item\.to\)">/)
    // 不清零就会出现 2px outset 黑边 + 灰底 + Arial 13.33px 的老式按钮
    expect(source).toMatch(/\.dh-crumb-link \{[\s\S]{0,300}?appearance: none;/)
    expect(source).toMatch(/\.dh-crumb-link \{[\s\S]{0,300}?-webkit-appearance: none;/)
    expect(source).toMatch(/\.dh-crumb-link \{[\s\S]{0,300}?font: inherit;/)
  })

  it('面包屑导航走 router.push（真实跳转，不是 cursor:pointer 的假可点）', () => {
    expect(source).toMatch(/function goCrumb\(to: string\) \{\s*router\.push\(to\)/)
    expect(source).toContain('aria-label="面包屑"')
    expect(source).toContain('class="dh-crumb-current"')
    // 反向：面包屑不得用 ellipsis 裁掉「边缘设备管理」
    expect(source).not.toMatch(/\.dh-crumb-link \{[^}]*text-overflow: ellipsis/)
    expect(source).not.toMatch(/\.dh-crumb[^{]*\{[^}]*text-overflow: ellipsis/)
  })
})
