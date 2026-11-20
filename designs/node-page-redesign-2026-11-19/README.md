# 节点页面 · 交互设计稿（2026-11-19）

直接打开 [index.html](index.html) 即可查看（纯静态，无构建、无外部依赖）。

## 内容

- 8 个页签：基本信息、总线与通道、外设、DMA 分配、关联设备、固件升级、日志、终端。
- 可交互：切换总线类型、选中资源、新建/配置通道（抽屉）、DMA 绑定/解绑、GPIO 读写、PWM 启停与占空比、设备筛选、OTA 筛选与取消、日志实时/历史、终端发送。
- 桌面 1440 与移动 390 两档布局。

## 截图

`shots/` 下为 Playwright 实拍；`before.png` 为改造前页面（对照用）；`shots/demo.gif` 为 10 秒交互录屏（页签切换、新建通道抽屉、I2C 扫描、GPIO 写入）。

## 说明

- 这是**设计稿**，不是生产代码；数据为演示数据，接口均为本地模拟。
- 重拍截图：`node capture.mjs`；设计 QA：`node verify.mjs`（Tab 宽度漂移、字号档位、控制台错误）。
- 细节规范（字体标尺、动效、图标语义）见设计文档 §5.1–5.3。
- 设计说明与验收见 [docs/设计/节点页面-交互重设计-2026-11-19.md](../../docs/设计/节点页面-交互重设计-2026-11-19.md)。

## 生产落地（2026-11-20）

已按本稿落到 `frontend-shared`，落地范围与验证见设计文档 §11。`implementation/` 下为**生产组件**的真实页面截图（Vite dev + mock API；桌面 1440×1000、移动 390×844）：

- [bus.png](implementation/bus.png)：总线与通道（UART，含通道与挂载设备）
- [periph.png](implementation/periph.png)：外设（GPIO/PWM 分段）
- [dma.png](implementation/dma.png)：DMA 分配表
- [mobile-bus.png](implementation/mobile-bus.png)、[mobile-dma.png](implementation/mobile-dma.png)：移动端
- [real-node-bus.png](implementation/real-node-bus.png)、[real-node-basic.png](implementation/real-node-basic.png)：本机 Docker 部署接入真机 C6 后的实拍（真实缓存/能力/状态）
- [periph-filter.png](implementation/periph-filter.png)：外设筛选修复后的分段控件（无相邻分隔线）
- [gpio-control.png](implementation/gpio-control.png)：真机 GPIO 写入 HIGH 后的开关与状态回填

> mock 截图仅用于核对布局与交互；`real-node-*` 为真实节点数据。
