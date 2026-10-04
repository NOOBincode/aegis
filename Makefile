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
	mcp-servers/runbook

.PHONY: help lint test test-integration build up down clean

help: ## 目标一览
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

lint: ## golangci-lint（.golangci.yml 唯一事实源，含 depguard 架构规则）
	golangci-lint run

test: ## 单元测试（-race 默认开启；goleak 由并发用例自管）
	@for m in $(GO_MODULES); do (cd $$m && go test -race ./...) || exit 1; done

test-integration: ## 集成测试（kind 实集群，M1 起填充用例）
	@echo "integration tests land from M1 (T1.2); kind cluster required"
	@exit 1

build: ## 构建全部 module（go.work 聚合的全部进程与库）
	@for m in $(GO_MODULES); do (cd $$m && go build ./...) || exit 1; done

up: ## 一键起环境：kind + 组件 + runsc 冒烟 + 健康摘要（T0.2 接通后生效）
	bash scripts/env-up.sh

down: ## 逆序销毁环境，幂等可重入
	bash scripts/env-down.sh

clean: ## 清理构建产物
	@for m in $(GO_MODULES); do (cd $$m && go clean ./...) || exit 1; done
	rm -rf bin/
