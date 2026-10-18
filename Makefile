# aegis 根 Makefile —— 薄封装，一切逻辑在 scripts/（scripts/README.md §4.3）。
# 跨平台：bash only（落地方案附录 B-1）；本地与 CI 同源跑同一目标（aegis-ci）。

GO_MODULES := . \
	cmd/aegis-cli \
	cmd/controller \
	cmd/gatekeeper \
	mcp-servers/events \
	mcp-servers/gitops \
	mcp-servers/k8s-read \
	mcp-servers/k8s-write \
	mcp-servers/logs \
	mcp-servers/prom \
	mcp-servers/runbook \
	mcp-servers/scan

.PHONY: help lint test test-integration build mcp-images mcp-up up down clean

help: ## 目标一览
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

# 逐 module 跑：根 module 的 `golangci-lint run` 不会覆盖 cmd/* 与 mcp-servers/*
# 这些独立 module（go.work 的 use 列表不等于包加载范围）。scan 漏在旧 GO_MODULES
# 之外正是"新增 server 没进 CI"的实例。
lint: ## golangci-lint（.golangci.yml 唯一事实源，含 depguard 架构规则；逐 module）
	@for m in $(GO_MODULES); do (cd $$m && golangci-lint run ./...) || exit 1; done

test: ## 单元测试（-race 默认开启；goleak 由并发用例自管）
	@go env CGO_ENABLED | grep -qx 1 \
		|| { echo "make test 需要 CGO_ENABLED=1 + C 编译器（-race 需要 cgo）。本地装 gcc 后重跑；CI ubuntu-latest 自带 gcc。"; exit 1; }
	@for m in $(GO_MODULES); do (cd $$m && go test -race ./...) || exit 1; done

test-integration: ## 集成测试（kind 实集群；需 KUBECONFIG 指向 aegis kind）
	@for m in mcp-servers/k8s-read mcp-servers/scan; do (cd $$m && go test -tags integration ./...) || exit 1; done

build: ## 构建全部 module（go.work 聚合的全部进程与库）
	@for m in $(GO_MODULES); do (cd $$m && go build ./...) || exit 1; done

mcp-images: ## 构建自研 MCP 镜像并 kind load（版本见 deploy/versions.env）
	bash scripts/build-mcp-images.sh

mcp-up: ## 部署 MCP server + RBAC（可选 --with-agent / --with-networkpolicy）
	bash scripts/mcp-up.sh

up: ## 一键起环境：kind + 组件 + runsc 冒烟 + 健康摘要（T0.2 接通后生效）
	bash scripts/env-up.sh

down: ## 逆序销毁环境，幂等可重入
	bash scripts/env-down.sh

clean: ## 清理构建产物
	@for m in $(GO_MODULES); do (cd $$m && go clean ./...) || exit 1; done
	rm -rf bin/
