package sanitize

import (
	"encoding/json"
	"fmt"
	"strings"
)

// I4 的隔离分隔符。Open/Close 必须成对出现，且不可出现在被包裹数据中——
// 集群侧写入的日志/事件 message 是不可信文本，若原样包含 Close 就能提前闭合
// 包裹、把后续内容伪装成"可信指令"（prompt injection 的经典边界突破）。
const (
	Open  = "<untrusted_cluster_data>"
	Close = "</untrusted_cluster_data>"
)

// Wrap 把集群返回数据包裹为不可信标记。map/struct 走 JSON（Go 的 encoding/json
// 默认 HTML 转义 < >，本身已能阻断分隔符）；string/[]byte 与 json.RawMessage 等
// 边界形态再经 Neutralize 兜底，保证数据内不可能出现字面量的 Open/Close。
//
// nil 返回 nil（不产出空包裹）；错误结果只承载 Err 时不调用本函数。
func Wrap(data any) any {
	if data == nil {
		return nil
	}
	var body string
	switch v := data.(type) {
	case string:
		body = v
	case []byte:
		body = string(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			body = fmt.Sprintf("%v", v)
		} else {
			body = string(b)
		}
	}
	return Open + Neutralize(body) + Close
}

// Neutralize 把数据中的 < / > 转成 JSON 的 \u003c/\u003e 形式，确保任何输入都
// 无法包含字面量 Open/Close。只做最小必要转义，不改动其它字符。
func Neutralize(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	// 先处理分隔符（它们包含在尖括号规则里），再处理残余尖括号；顺序无关，但
	// 显式保留注释，提醒后续维护者不要改成"只替换 Close"——Open 同样可被利用
	// 来伪造嵌套包裹。
	r := strings.NewReplacer(
		"<", `\u003c`,
		">", `\u003e`,
	)
	return r.Replace(s)
}

// Wrapped 是 Wrap 的字符串形态（测试/审计便于断言）。
func Wrapped(data any) string {
	v, _ := Wrap(data).(string)
	return v
}
