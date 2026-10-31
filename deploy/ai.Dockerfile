# syntax=docker/dockerfile:1
# srvmon-ai: the AI agent (pi SDK) with srvmon tools only. Talks to
# srvmon-server over the compose network; no ports are published.

# The build stage runs on the runner's native platform: npm ci under QEMU
# (arm64 on an amd64 runner) can hang for hours. node_modules here are pure JS
# (no os/cpu-specific packages, install scripts disabled), so the result is
# copied into the target-platform runtime stage unchanged.
FROM --platform=$BUILDPLATFORM node:22-alpine AS build
WORKDIR /src/ai
COPY ai/package.json ai/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY ai/ ./
RUN npm run build && npm prune --omit=dev --ignore-scripts

FROM node:22-alpine
ARG VERSION=dev
ENV NODE_ENV=production \
    SRVMON_VERSION=${VERSION} \
    SRVMON_AI_DATA=/data \
    HOME=/data
WORKDIR /app
COPY --from=build /src/ai/node_modules ./node_modules
COPY --from=build /src/ai/dist ./dist
COPY ai/package.json ./
RUN mkdir -p /data && chown node:node /data
USER node
VOLUME ["/data"]
EXPOSE 8090
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["node", "-e", "fetch('http://127.0.0.1:'+(process.env.SRVMON_AI_LISTEN||8090)+'/healthz').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"]
ENTRYPOINT ["node", "dist/main.js"]
