# T2.7 注入检测与 R2 升级信号

## Goal

在 I4 包裹/消毒之外补“检测信号”：识别指令型/角色扮演/围栏逃逸文本，作为
MCP 出口元数据与审计字段；T2.8 gatekeeper 据此把该数据衍生的写动作升 R2。

## Requirements

1. `internal/sanitize.DetectInjection`：指令忽略、角色切换、system prompt、
   delete namespace、kubectl delete、curl|sh、rm -rf /、围栏逃逸；只返回模式 ID。
2. MCP 出口 `Meta.injection_hit/injection_patterns`；审计日志同字段。
3. DecisionRecord `toolCalls[].injectionHit/InjectionPatterns` + CRD schema + replay 渲染。
4. 单测覆盖恶意/正常样例；不得把命中原文写入日志（只写 pattern ID）。
5. R2 升级动作在 T2.8 gatekeeper 执行；本任务只供信号。

## Acceptance Criteria

- [x] 检测器单测：恶意样例命中、正常 OOM 日志不误报。
- [x] MCP 中间件设置 Meta 字段；审计 JSONL 含 injection_hit/patterns。
- [x] DecisionRecord/CRD/replay 字段贯通；lint 0。
- [ ] gatekeeper 把 hit 转 R2（T2.8）。

## Notes

- 设计出处：§4.2.2 log-sanitizer、I4/F2；模式库可评审扩展。
