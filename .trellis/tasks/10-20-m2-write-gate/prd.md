# M2 安全写闸门（CRD/风险/审批/回滚/闸门）

## Goal

M2 把 aegis 从“只读诊断”推进到“受控安全写”：ChangeRequest/GuardrailPolicy 契约、
确定性风险分级、策略/审批、逆操作/回滚、熔断/配额、gatekeeper MCP 代理、authority 归因、
GitOps 通道与 mTLS 信封；M2 红队检查点全量通过后才进入 M3。

## Task Map

- T2.1 CRD + 状态机（本目录子任务）
- T2.2 risk-classifier / 确定性风险表
- T2.3 GuardrailPolicy / OPA / 审批
- T2.4 CLI approve/deny（无绕过）
- T2.5 逆操作 / 回滚 / 漂移复验
- T2.6 熔断 / 爆炸半径配额
- T2.7 sanitizer / 注入命中升 R2
- T2.8 gatekeeper MCP 代理
- T2.9 authority/attributor（先归因后行动）
- T2.10 GitOps/ArgoCD 强制 PR 通道
- T2.11 信封签名 / 序号 / mTLS
- T2.12 M2 红队检查点（审批绕过/注入/越权/重放/预算）

## Acceptance Criteria（M2 总门）

- [ ] 所有写工具只接受 ChangeRequest ID；gatekeeper 是唯一放行点。
- [ ] R2/R3 无法被自写客户端/`--yes` 绕过；审批状态机在服务端。
- [ ] 写操作必有逆操作 + dry-run 验证；失败回滚复验。
- [ ] 配额/熔断/会话预算在执行路径强制，超限拒绝。
- [ ] 先归因后行动：控制器所有权字段直写拒绝。
- [ ] M2 红队检查点（R1/R3/R6/R17 等）全量过且整改完。
