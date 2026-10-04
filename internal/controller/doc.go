// Package controller 是 reconcile 层：读 CR → 调应用服务 → 写 status，保持薄（铁律 3）。
// M2（T2.1）接入 controller-runtime；leader election 单写者（I10）。
package controller
