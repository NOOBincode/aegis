// Package securitytest 是 M2 红队检查点的纯领域对抗测试集合（T2.12 的一部分）。
// 它不是生产包；只验证 risk/policy/approval/gatekeeper/envelope/sanitizer 的
// “绕过变体”行为，防止安全组件在重构中退化。
package securitytest
