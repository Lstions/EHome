import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

/**
 * 对话框/轻提示的**视口可达性**契约（2026-10-21 真机实测缺陷）。
 *
 * 两类缺陷都属于「用户根本用不了」，但都不是业务逻辑错误，靠功能测试发现不了：
 *  ① /automation「创建规则」对话框 640×926px，在 1440×851 与 1440×900 下超出视口
 *     （bottom 1054/1061 > vh），且 body 不可滚 ⇒ 底部「保存/取消」点不到；
 *     根因：项目只约束了 dialog 宽度（max-width:92vw），从没约束高度。
 *  ② 缺 element-plus/theme-chalk/el-message.css ⇒ ElMessage 计算样式 position:static，
 *     被追加到 body 末尾渲染在视口下方（rect.y=851/900），所有轻提示用户都看不到。
 *     unplugin-vue-components 按需引入只处理**组件**，JS API（ElMessage/ElMessageBox）的 CSS 必须手动导入。
 */
const themeCss = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')
const mainTs = readFileSync(resolve(process.cwd(), 'src/main.ts'), 'utf8')

describe('对话框不得超出视口、footer 必须可达', () => {
  it('dialog 有视口高度上限（100vh/100dvh 扣减）', () => {
    expect(themeCss).toMatch(/\.el-dialog \{[^}]*max-height:\s*calc\(100vh - 32px\)/)
    expect(themeCss).toMatch(/\.el-dialog \{[^}]*max-height:\s*calc\(100dvh - 32px\)/)
  })

  it('dialog 用纵向 flex，header/footer 不收缩、body 内部滚动', () => {
    expect(themeCss).toMatch(/\.el-dialog \{[^}]*display:\s*flex/)
    expect(themeCss).toMatch(/\.el-dialog \{[^}]*flex-direction:\s*column/)
    expect(themeCss).toMatch(/\.el-dialog__header,\s*\n?\.el-dialog__footer \{[^}]*flex:\s*0 0 auto/)
    expect(themeCss).toMatch(/\.el-dialog__body \{[^}]*flex:\s*1 1 auto/)
    expect(themeCss).toMatch(/\.el-dialog__body \{[^}]*overflow-y:\s*auto/)
  })

  it('遮罩层居中 + dialog 纵向 margin 收敛（否则 EP 的 margin-top:15vh 会吃掉高度上限）', () => {
    expect(themeCss).toMatch(/\.el-overlay-dialog \{[^}]*display:\s*flex/)
    expect(themeCss).toMatch(/\.el-overlay-dialog \{[^}]*align-items:\s*center/)
    // 实测：只加 max-height 时 819+128=947 仍溢出，必须同时把 margin-top:15vh 收成 16px
    expect(themeCss).toMatch(/\.el-dialog \{[^}]*margin:\s*16px auto !important/)
  })
})

describe('ElMessage 的 CSS 必须显式导入（按需引入不覆盖 JS API）', () => {
  it('main.ts 导入 el-message.css 与 el-message-box.css', () => {
    expect(mainTs).toContain("import 'element-plus/theme-chalk/el-message.css'")
    expect(mainTs).toContain("import 'element-plus/theme-chalk/el-message-box.css'")
  })

  it('导入位置在应用挂载之前（否则首屏提示仍会丢样式）', () => {
    const msgIdx = mainTs.indexOf("element-plus/theme-chalk/el-message.css")
    const mountIdx = mainTs.indexOf('.mount(')
    expect(msgIdx).toBeGreaterThan(-1)
    expect(mountIdx).toBeGreaterThan(msgIdx)
  })
})