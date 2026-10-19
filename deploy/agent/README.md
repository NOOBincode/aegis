# deploy/agent — Agent 运行时配置（L3）

> 里程碑：M1 T1.6 ｜ 设计出处：§4.3、ADR-002、R24
> 安全：本目录的清单**不含 API key**；key 由 `scripts/ai-up.sh` 从环境/交互输入写入
> `kagent/aegis-llm` Secret。数据出域与日志纪律见 `docs/data-egress.md`。

## 文件

| 文件 | 用途 |
| --- | --- |
| `modelconfig.yaml` | ModelConfig（OpenAI 兼容；默认 DeepSeek 示例），引用 `aegis-llm` Secret |
| `agent.yaml` | `aegis-diagnoser` Agent（systemMessage + 只读工具白名单） |

## 配置/轮换

```bash
# 推荐：key 只经环境变量，不落 shell 历史/命令行参数
AEGIS_LLM_API_KEY='...' bash scripts/ai-up.sh --with-agent

# 或交互式（输入不回显）
bash scripts/ai-up.sh --with-agent

# 可选覆盖
AEGIS_LLM_BASE_URL=http://vllm.internal:8000/v1 \
AEGIS_LLM_MODEL=qwen2.5-32b-instruct \
AEGIS_LLM_API_KEY='...' bash scripts/ai-up.sh --with-agent --restart
```

- 外部 AI 意味着数据出域；使用前读 `docs/data-egress.md`，敏感环境请把
  `AEGIS_LLM_BASE_URL` 指向私域端点并封禁公网 AI 出口。
- Agent 的工具白名单：`scan_cluster` + 8 个只读 k8s-read 工具 + `prom_query`/`prom_range` + `aggregate_logs` + `get_event_timeline`（共 13 个）；不得加入写工具。
- 轮换/删除：删除 `kagent/aegis-llm` 后 Agent 不可用；恢复用同一命令。
