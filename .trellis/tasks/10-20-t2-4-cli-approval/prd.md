# T2.4 CLI approve/deny

## Goal

CLI 只提交带 HMAC 信封的人审意图，不实现审批状态机、不写 CR status、不提供 `--yes`
绕过；审批合法性/身份绑定/终判在服务端（R16/R1）。

## Requirements

1. `aegis-cli approve <ns/name>` / `deny <ns/name>`；`--note` 可审计。
2. `AEGIS_APPROVAL_SECRET` 为 ≥16 字节 base64 密钥；信封经 `internal/session.Signer` 签名。
3. `--endpoint`/`AEGIS_APPROVAL_ENDPOINT` 必填，http(s) 校验；超时限制。
4. actor 必须显式注入（`--actor`/`AEGIS_ACTOR`）；无 actor 拒绝；CLI 不猜身份。
5. CLI 不包含放行/拒绝判定，也不直接写 K8s；只 POST 签名信封。
6. httptest 验证服务器能解信封、payload 正确；缺 secret 明确失败。

## Acceptance Criteria

- [x] CLI approve/deny + HMAC 信封 + http(s) endpoint + actor 必填。
- [x] CLI 无 `--yes`/本地判定；缺 secret 拒绝。
- [x] httptest 校验签名与 payload；lint 0。
- [x] gatekeeper `/v1/approval` HTTP 入口：验签→approval.Decide→写 status.approval；fake dynamic 测试通过。
- [ ] mTLS 身份绑定（当前 actor 来自签名 payload，T2.11 传输层待接）。
- [x] CLI→ApprovalServer→status.approval 端到端测试通过（httptest + fake dynamic）；审批人/状态断言。

## Notes

- 设计出处：§4.2.1 R1、R16；本任务先做安全客户端契约。

- 2026-10-20 R2 审批 E2E：CLI `approve --cr kagent/...` 成功；服务端写 Approved；controller 仅审批后才执行（replicas 1→3，Committed）。

- 2026-10-20：kagent MCPServer `kagent/gatekeeper` 已部署为 MCP-only（request_change 可达）；审批端口需独立 Secret/部署，当前未在集群 gatekeeper 内启用。

- 2026-10-21：集群内审批 Deployment + Secret volume 实测：CLI（从集群 Secret 注入 HMAC）审批 R2，controller 执行成功。
