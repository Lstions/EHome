# Build stage
FROM node:22-alpine AS builder

WORKDIR /app

# Install pnpm
RUN corepack enable && corepack prepare pnpm@latest --activate

# Copy package files
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./

# Install dependencies
RUN pnpm install --frozen-lockfile

# Copy source and build
COPY . .
RUN pnpm build

# Runtime stage: Node proxy server with static file serving
FROM node:22-alpine

WORKDIR /app

COPY --from=builder /app/dist /app/dist
COPY server.js /app/server.js

EXPOSE 80

ENV BACKEND_URL=http://backend:8080

CMD ["node", "server.js"]
