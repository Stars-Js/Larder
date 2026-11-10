# 项目基本信息
BINARY   := larder
CMD_DIR  := .
GO       ?= go

.PHONY: help
help: ## 显示可用命令
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## 整理依赖
	$(GO) mod tidy

.PHONY: fmt
fmt: ## 格式化代码
	$(GO) fmt ./...

.PHONY: vet
vet: ## 静态检查
	$(GO) vet ./...

.PHONY: test
test: ## 运行测试（含竞态检测）
	$(GO) test -race -covermode=atomic -coverprofile=coverage.out ./...

.PHONY: cover
cover: test ## 查看覆盖率报告
	$(GO) tool cover -html=coverage.out

.PHONY: lint
lint: fmt vet test ## 一键检查

.PHONY: build
build: ## 编译到 bin/
	$(GO) build -trimpath -ldflags "-s -w" -o bin/$(BINARY)$(EXE) $(CMD_DIR)

.PHONY: windows
windows: ## 编译 Windows x64 桌面版到 dist/larder-windows-amd64.exe
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "-s -w -H windowsgui" -o dist/larder-windows-amd64.exe $(CMD_DIR)

.PHONY: windows-arm64
windows-arm64: ## 编译 Windows ARM64 桌面版到 dist/larder-windows-arm64.exe
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 $(GO) build -trimpath -ldflags "-s -w -H windowsgui" -o dist/larder-windows-arm64.exe $(CMD_DIR)

.PHONY: release
release: windows windows-arm64 ## 构建两个架构的发布包与 SHA256SUMS 到 release/
	mkdir -p release
	cp dist/larder-windows-amd64.exe dist/larder-windows-arm64.exe release/
	cd release && sha256sum larder-windows-amd64.exe larder-windows-arm64.exe > SHA256SUMS

.PHONY: run
run: ## 本地运行
	$(GO) run $(CMD_DIR)

.PHONY: clean
clean: ## 清理构建产物
	$(GO) clean
	rm -rf bin release coverage.out coverage.html
