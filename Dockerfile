# 前端构建
FROM node:24-alpine AS web
WORKDIR /build
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN cd web && pnpm install --frozen-lockfile
COPY web/ ./web/
RUN mkdir -p internal/web/dist && cd web && pnpm build

# 后端构建，前端产物在编译前放进 embed 目录
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /build/internal/web/dist ./internal/web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tiro ./cmd/tiro

# 运行镜像
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 tiro && \
    mkdir -p /data && chown tiro /data
ENV TZ=Asia/Shanghai \
    TIRO_DATA_DIR=/data
USER tiro
WORKDIR /app
COPY --from=build /out/tiro /app/tiro
VOLUME /data
EXPOSE 8476
ENTRYPOINT ["/app/tiro"]
