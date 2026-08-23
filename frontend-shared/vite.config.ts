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
  const apiTarget = env.VITE_API_TARGET || 'http://localhost:8082'
  const wsTarget = env.VITE_API_TARGET
    ? env.VITE_API_TARGET.replace('http://', 'ws://')
    : 'ws://localhost:8082'

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
      '/api': {
        target: apiTarget,
        changeOrigin: true,
        ws: true,
      },
      '/ws': {
        target: wsTarget,
        ws: true,
      },
      // 反代前缀场景：/ehome-dev/api -> 后端 /api（剥离前缀）
      ...(basePrefix ? {
        [`${basePrefix}/api`]: {
          target: apiTarget,
          changeOrigin: true,
          ws: true,
          rewrite: (path: string) => path.replace(new RegExp(`^${basePrefix}`), ''),
        },
        [`${basePrefix}/ws`]: {
          target: wsTarget,
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
