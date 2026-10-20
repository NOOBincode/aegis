# T2.11 签名信封

## Goal

M2.5-4 通道安全三件套的软件层：HMAC 签名 + 会话内单调序号 + 时间窗，防重放/串话；
mTLS 由部署层（证书仅签 gatekeeper）另行强制，二者不互相替代。

## Requirements

1. `internal/session.Signer.Seal` / `Verifier.Open`；secret ≥16 字节。
2. 签名覆盖 session/seq/issued_at/payload；HMAC equal 常量时间比较。
3. 重放/序号回退→ErrReplay；时间窗外→ErrExpired；篡改→ErrBadSignature。
4. 序号状态按 session 隔离，进程内存可重建；密钥不落日志。
5. 单测覆盖正常/重放/回滚/篡改/过期；lint 0。

## Acceptance Criteria

- [x] seal/open 往返；payload 完整。
- [x] 重放、序号回退、篡改、过期全部拒绝。
- [x] 单测通过；lint 0。
- [ ] 接入 gatekeeper MCP 传输层（T2.8）与 mTLS 证书体系。

## Notes

- 设计出处：§4.2.5-4、F15；HMAC 是软件层，mTLS 是传输层。
