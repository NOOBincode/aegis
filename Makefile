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

.PHONY: help lint test test-integration build mcp-images mcp-up security-smoke redteam-netpol check-skills check-secrets ai-up ai-down m1-demo up down clean

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

security-smoke: ## 安全冒烟：RBAC/kagent-tools/NetworkPolicy/MCP 工具面（需 kind+kagent）
	bash scripts/security-smoke.sh

redteam-netpol: ## 网络红队：5 个 MCP server 的 default 阻断/kagent 放行（需 kind+MCP）
	bash scripts/redteam-netpol.sh

check-skills: ## 开发期 Skill 污染检查（lock 覆盖/symlink/可执行资产/安装型管道）
	bash scripts/check-skills.sh

check-secrets: ## 密钥泄漏检查（当前树；--history 另跑）
	bash scripts/check-secrets.sh

ai-up: ## 安全配置/轮换 LLM key（Secret）+ ModelConfig（key 不落 git/日志）
	bash scripts/ai-up.sh --with-agent

ai-down: ## 删除 LLM key（止损/轮换前）
	bash scripts/ai-down.sh

m1-demo: ## M1 验收：OOM + scan + Agent + 审计 replay（需 kind；Agent 可选）
	bash scripts/m1-demo.sh

up: ## 一键起环境：kind + 组件 + runsc 冒烟 + 健康摘要（T0.2 接通后生效）
	bash scripts/env-up.sh

down: ## 逆序销毁环境，幂等可重入
	bash scripts/env-down.sh

clean: ## 清理构建产物
	@for m in $(GO_MODULES); do (cd $$m && go clean ./...) || exit 1; done
	rm -rf bin/
