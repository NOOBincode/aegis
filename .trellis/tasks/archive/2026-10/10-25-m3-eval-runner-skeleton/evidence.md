# M3 eval runner 骨架验证证据

日期：2026-10-25（Asia/Shanghai）

## 通过

```bash
python3 -m unittest discover -s eval/tests -v
# Ran 25 tests ... OK

make test-eval
# Ran 25 tests ... OK；exit 0

GOCACHE="$PWD/.cache/gocache" GOLANGCI_LINT_CACHE="$PWD/.cache/lint" make lint
# 0 issues；exit 0

GOCACHE="$PWD/.cache/gocache" make test-unit
# 所有 Go module 通过；exit 0

GOCACHE="$PWD/.cache/gocache" make build
# exit 0（module stat cache 权限 warning，不影响构建）
```

## 真实 kind smoke（环境阻断，不改成 pass）

```bash
GOCACHE="$PWD/.cache/gocache" make eval-smoke
```

- 场景 `oom-killed`：`diagnostic_hit=true`、`object_hit=true`、`root_cause_hit=true`。
- run 状态仍为 `infrastructure_failure`：`cleanup_error` 为
  `kubectl delete namespace aegis-eval --ignore-not-found` 60s 超时；
  当时 `aegis-eval` 因 `metrics.k8s.io/v1beta1` stale discovery 卡在 Terminating。
- 未人工修改为 pass；cleanup 失败按 fail-closed 分类。
- 运行归档：`.cache/eval/runs/20261025T025801340753Z-5689721e247c`（ignored cache）。

## 边界

- 不是 M3 全量验收：仅 T3.1 单场景 runner/grader/归档骨架。
- 不含 20 场景治理、held-out 轮换、完整语义 grader、CI 回归门禁。
- 未新增 Go 依赖，eval 不 import Go 内部包。
