import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import { fileURLToPath, URL } from 'node:url'

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  // 统一从 .env* 文件读取环境变量（与前端 import.meta.env 行为一致）。
  // 注意：不能用 process.env 读 VITE_* —— vite 不会把 .env 文件注入 process.env，
  // 用 process.env 会导致 vite.config 与前端 import.meta.env 读到不一致的值。
  const env = loadEnv(mode, process.cwd(), '')
  // 默认代理到 :8080 的 ehome-web 容器（前端 SPA + 后端 API 同容器，且是唯一
  // 连着 EMQX、真正在收设备数据的实例）。
  // ⚠️ 曾经默认 :8082，但该端口已被**无关项目** digital-family-tree 的容器占用：
  // 代理指过去时端口确实有服务在监听，因此不会报错，只会 REST 404 + WebSocket
  // 永久握手失败 ⇒ 界面「一直显示离线」。默认值必须指向真正的 EHome 后端。
  const apiTarget = env.VITE_API_TARGET || 'http://localhost:8080'
  const wsTarget = env.VITE_API_TARGET
    ? env.VITE_API_TARGET.replace('http://', 'ws://')
    : 'ws://localhost:8080'

  // 子路径前缀部署（反代 /ehome-dev 场景）：VITE_BASE_PATH=/ehome-dev/ 时启用
  const basePath = env.VITE_BASE_PATH || '/'
  // 去掉首尾斜杠，用于 proxy key 拼接（如 /ehome-dev/api）
  const basePrefix = basePath === '/' ? '' : basePath.replace(/\/+$/, '')

  return {
  plugins: [
    vue(),
    // Element Plus 按需自动引入
    AutoImport({
      imports: ['vue', 'vue-router', 'pinia'],
      resolvers: [ElementPlusResolver()],
      dts: 'src/auto-imports.d.ts',
    }),
    Components({
      resolvers: [
        // 自动按需引入 Element Plus 组件和样式
        ElementPlusResolver({ importStyle: 'css', dts: 'src/components.d.ts' }),
      ],
      // 自定义组件位置（项目内）
      dirs: ['src/components'],
      dts: 'src/components.d.ts',
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  // 子路径前缀部署（反代 /ehome-dev 场景）
  base: basePath,
  server: {
    host: '0.0.0.0',
    // 放行任意 Host 访问 dev server（自定义域名/反代入口，如 console.qsun.fun）
    allowedHosts: true,
    port: 5174,
    proxy: {
      // baseURL 已含 /api/v1, 所以只代理根路径下的后端
      //
      // ⚠️ `changeOrigin` 必须为 **false**（此处显式写出，防止被"顺手改成 true"）：
      // 后端 WebSocket 升级有 CSRF 防护 —— `checkOrigin` 要求
      // `Origin.host === r.Host`（backend/internal/websocket/websocket.go:82），
      // 否则 403 "request origin not allowed by Upgrader.CheckOrigin"。
      //
      // changeOrigin:true 会把 Host 改写成后端地址（127.0.0.1:8080），
      // 而浏览器发的 Origin 仍是 dev server 地址（127.0.0.1:5174）⇒ 两者不等 ⇒
      // **WS 必然 403**，前端 NetworkBanner 持续显示
      // 「与服务器的连接已断开／正在尝试重新连接...」并每 10s 重连。
      // 保持 false 则 Host 原样透传，与 Origin 一致 ⇒ 101 Switching Protocols。
      //
      // 为什么不改后端放行：生产部署前端 dist 与后端**同容器同端口**
      // （EHOME_EXTERNAL_HOST=<host>:8080），同源校验在生产完全正确，属安全设计
      // （提交 6b764ffa 加固）。只有 dev 的"5174 前端 + 8080 后端"跨源场景需要对齐，
      // 故在代理侧解决。
      //
      // 验证提示：`curl` 不发 Origin 头时后端直接放行（`origin == ""` → true），
      // 因此用 curl 测 WS 会**假绿** —— 必须用真实浏览器或显式带 Origin 才测得准。
      '/api': {
        target: apiTarget,
        changeOrigin: false,
        ws: true,
      },
      '/ws': {
        target: wsTarget,
        changeOrigin: false,
        ws: true,
      },
      // 反代前缀场景：/ehome-dev/api -> 后端 /api（剥离前缀）
      ...(basePrefix ? {
        [`${basePrefix}/api`]: {
          target: apiTarget,
          changeOrigin: false,
          ws: true,
          rewrite: (path: string) => path.replace(new RegExp(`^${basePrefix}`), ''),
        },
        [`${basePrefix}/ws`]: {
          target: wsTarget,
          changeOrigin: false,
          ws: true,
          rewrite: (path: string) => path.replace(new RegExp(`^${basePrefix}`), ''),
        },
      } : {}),
    },
  },
  build: {
    // 提升主 chunk 体积阈值到 1.5MB（ECharts 部分按需引入后会远低于此）
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        // ECharts / xterm / 业务通用 chunk 拆分
        manualChunks(id) {
          if (id.includes('node_modules/echarts/')) return 'echarts'
          if (id.includes('node_modules/element-plus/')) return 'element'
          if (id.includes('node_modules/vue/') || id.includes('node_modules/vue-router/') || id.includes('node_modules/pinia/')) return 'vue'
        },
      },
    },
  },
  }
})
