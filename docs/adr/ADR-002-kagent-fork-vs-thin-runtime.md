---
adr_id: ADR-002
title: Agent 运行时 kagent 二开 vs 自研薄运行时
status: accepted
date: 2026-09-22
supersedes: —
review_due: 每个里程碑末复评；M1 末为第一个固定复评点
---

# ADR-002：Agent 运行时 kagent 二开 vs 自研薄运行时

> 转录自设计文档附录 A（落地方案 T0.6）；原文出处 `AI原生K8s安全操作平台-技术方案设计文档.md` 附录 A。
> 关联不变量：I2（LLM 不进热路径）、I6（决策可回放）｜ 关联：ADR-001；kagent 版本钉死见 `deploy/versions.md`（钉 release tag，禁止跟踪 main）。

## 1. 背景

L3 Agent 运行时底座两条路：基于 kagent 二开（复用 Agent CRD + ADK 引擎 + ToolServer 抽象），还是自研薄运行时。kagent 提供声明式 Agent、工具白名单、OTel 追踪等现成件，且是 CNCF 项目、可写进叙事；自研薄运行时练手价值高，但与本项目主线（L2 安全闸门）抢时间。M1 集成开工前必须拍板。

## 2. 决策

M1–M3 基于 kagent 做**配置层二开**，不改其 controller 内核，复用 Agent/ToolServer CRD 与 ADK 引擎。适用范围：L3 Agent 运行时底座选型（T0.4 落盘 fork 边界）。

## 3. 代价与接受

代价是叙事上可能被读为"搭积木"，且绑定 kagent 上游演进节奏。换来的是 L3 现成件省下的时间全部投入 L2 闸门与 L4 eval——差异化叙事正是锁定在这两处自研（设计文档 §9 末行 / 红队 R8），接受。

## 4. 反方意见留档

降级备选不删除：仅复用 kagent 的 ToolServer/MCP 生态，自研薄运行时（约 +40h，叙事反而增强）。命中复评触发条件即复活。

## 5. 复评触发条件

- kagent 上游架构剧变；或
- fork 维护成本 > 20h/月。

命中任一 → 降级为"仅复用其 ToolServer/MCP 生态 + 自研薄运行时"。本决策随每个里程碑复评；M1 末为第一个固定复评点（落地方案 §4 固定动作：统计 kagent 集成实际耗时与阻塞点，产出可执行的降级判断而非含糊其辞）。

## 复评记录

- 2026-09-22 设计文档 v1.4 定稿，决策确立（附录 A）；fork 边界"仅配置层二开、不改 controller 内核"随 T0.4 落盘。

## 修订历史

- 2026-10-04 自设计文档附录 A 转录为独立 ADR 文件（T0.6）。
