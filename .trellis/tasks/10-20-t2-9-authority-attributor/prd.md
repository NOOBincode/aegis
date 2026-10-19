# T2.9 authority-map + attributor 纯领域

## Goal

I9 的核心：字段级所有权图谱回答“这个字段归谁管”，attributor 回答“这次变化是谁干的”。
归因到健康控制器的变化视为非事件，不进诊断/不触发写；解释不了的残差才交 L3。

## Requirements

1. `FieldRef`/`Owner`/`Graph`：AddOwner/显式 MarkClear/Owners/Check 三态。
2. Check 保守默认：无 owner 证据 → via-owner-api；显式 clear 才 clear；不猜。
3. `Translate`：scale+HPA/KEDA → minReplicas；resources+VPA → VPA policy；
   ArgoCD → GitOps PR；未知/无 owner → rejected（fail-closed）。
4. `Attribute`：owner + 时间窗 + 方向/幅度一致 → attributed；有证据不吻合 → partial；
   无证据/窗口外 → unattributed。
5. 零 LLM/k8s import；领域纯单测。
6. informer/Events/指标接线与“HPA 正常伸缩 Agent 零动作”kind 场景留后续。

## Acceptance Criteria

- [x] 图谱无证据保守 via-owner-api；显式 clear 放行；HPA owner 识别。
- [x] 翻译规则 HPA/VPA/ArgoCD/未知/无 owner 全组合。
- [x] 归因 attributed/partial/unattributed 边界（窗口/方向/幅度）。
- [x] 单测通过；lint 0。
- [ ] informer 增量刷新、Events/audit/指标接线、T2.9 kind 判据。

## Notes

- 设计出处：§4.2.2/§4.2.4、I9/F11/F13；L2 安全包，需人审。
