package scan

import _ "embed"

// DefaultReliabilityYAML 是嵌入的默认 B 层规则包（reliability + security 子集）。
// 仅承载规则字节；YAML 解码在装配边缘（cmd 层 FilePolicySource）完成——
// 本包不解码（铁律 2 领域纯净：不依赖 sigs.k8s.io/yaml）。
//
// 默认规则随二进制分发，保证气隙/离线环境开箱即有基线规则；磁盘上的规则目录
// 可覆盖/增补（热修改），实现见装配边缘的 FilePolicySource。
//
//go:embed policies/reliability.yaml
var DefaultReliabilityYAML []byte
