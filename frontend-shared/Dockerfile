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

# Runtime stage: serve with node static server (avoids nginx pull from Docker Hub)
FROM node:22-alpine

WORKDIR /app
RUN corepack enable && corepack prepare pnpm@latest --activate

# Install serve globally
RUN npm install -g serve

COPY --from=builder /app/dist /app/dist

EXPOSE 80

CMD ["serve", "-s", "dist", "-l", "80"]
