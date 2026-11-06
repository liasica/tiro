GO ?= go
BIN := bin/tiro
GCLINT ?= /usr/local/bin/gclint
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help
help:
	@echo "可用目标："
	@echo "  make all     构建前端与后端，产出单文件二进制"
	@echo "  make build   只编译后端"
	@echo "  make web     只构建前端并输出到 internal/web/dist"
	@echo "  make run     本地运行，数据放在 data 目录"
	@echo "  make lint    检查 Go 与前端代码"
	@echo "  make image   构建 docker 镜像"
	@echo "  make clean   清理构建产物"

.PHONY: all
all: web build

.PHONY: build
build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/tiro

.PHONY: web
web:
	cd web && pnpm install && pnpm build

.PHONY: run
run:
	$(GO) run ./cmd/tiro

.PHONY: lint
lint:
	$(GCLINT) run --config .golangci.yml --timeout=10m
	cd web && pnpm lint

.PHONY: image
image:
	docker build --build-arg VERSION=$(VERSION) -t tiro:$(VERSION) .

.PHONY: clean
clean:
	rm -rf bin internal/web/dist/assets internal/web/dist/index.html
