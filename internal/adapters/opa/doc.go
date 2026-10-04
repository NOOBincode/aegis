// Package opa 是 OPA/Rego 适配器：策略求值的唯一实现位置（policies/ 为 bundle 源）。
// 策略超时/引擎不可用 → fail-closed 拒绝（设计文档 §4.2.2）。
package opa
