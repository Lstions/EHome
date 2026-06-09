FROM node:22-alpine

RUN corepack enable && corepack prepare pnpm@latest --activate

WORKDIR /app

# 仅安装依赖 (源码通过 bind mount 提供)
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

EXPOSE 5174

CMD ["pnpm", "dev", "--host", "0.0.0.0"]
