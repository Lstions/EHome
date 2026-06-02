# frontend - 前端应用

前端实现，提供实时监控、配置管理和数据可视化界面。

## 技术栈

| 技术 | 版本 | 说明 |
|------|------|------|
| Vue | 3.5+ | Composition API |
| TypeScript | 5+ | 类型安全 |
| Vite | 7+ | 构建工具 |
| Element Plus | 2+ | UI 组件库 |
| Pinia | - | 状态管理 |
| Vue Router | - | 路由管理 |
| Axios | - | HTTP 客户端 |
| ECharts | - | 图表库 |

## 目录结构

```
frontend/
├── src/
│   ├── api/            # API 客户端
│   ├── components/     # 通用组件
│   ├── composables/    # 组合式函数
│   ├── router/         # 路由配置
│   ├── stores/         # Pinia 状态管理
│   ├── utils/          # 工具函数
│   └── views/          # 页面组件
├── public/             # 静态资源
├── .env.development    # 开发环境变量
├── .env.production     # 生产环境变量
└── vite.config.ts      # Vite 配置
```

## 快速开始

```bash
# 安装依赖
pnpm install

# 开发模式
pnpm dev

# 构建生产版本
pnpm build

# 运行测试
pnpm test
```

## 访问地址

| 环境 | 地址 |
|------|------|
| 开发模式 | http://localhost:5174 |
| 默认账号 | admin / admin123 |

## 环境变量

`.env.development`:
```
VITE_API_BASE_URL=http://localhost:8080
VITE_WS_BASE_URL=ws://localhost:8080
```

## 功能模块

| 模块 | 路由 | 说明 |
|------|------|------|
| 登录 | /login | 用户认证 |
| 仪表盘 | /dashboard | 系统概览 |
| 采集器 | /collectors | 采集器管理 |
| 设备 | /devices | 设备管理 |
| 固件 | /firmware | 固件管理 |

## 开发规范

- 使用 Composition API (`<script setup>`)
- 使用 TypeScript 类型定义
- 组件命名：PascalCase
- Git 提交：feat/fix/docs/refactor/test/chore

## 相关文档

- [项目主文档](../README.md)
- [API 文档](../docs/api.md)

## License

MIT
