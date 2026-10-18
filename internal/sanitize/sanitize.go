package sanitize

import (
	"bytes"
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
		// SetEscapeHTML(false) 让 < / > 以字面量输出，再由 Neutralize 转成非 ASCII
		// 的全角字符；若用 json.Marshal 默认的 \u003c 转义，下游一旦 JSON 解码就会
		// 还原成 <，I4 分隔符仍可被伪造。
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			body = fmt.Sprintf("%v", v)
		} else {
			body = strings.TrimSuffix(buf.String(), "\n")
		}
	}
	return Open + Neutralize(body) + Close
}

// Neutralize 把数据中的 ASCII 尖括号（以及 JSON 的 \u003c/\u003e 转义）转成
// 全角 ＜/＞，确保无论下游是否 JSON 解码，都无法得到字面量 Open/Close。
// 只做最小必要转义，不改动其它字符。
func Neutralize(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	// 显式替换 Open/Close 本身也覆盖了；注意不要改成"只替换 Close"——Open 同样
	// 可被利用来伪造嵌套包裹。\u003c/\u003e（及大写变体）也一并替换，防止
	// 下游 JSON 解码把转义还原成 ASCII 尖括号。
	r := strings.NewReplacer(
		"<", "＜",
		">", "＞",
		`\u003c`, "＜",
		`\u003e`, "＞",
		`\u003C`, "＜",
		`\u003E`, "＞",
	)
	return r.Replace(s)
}

// Wrapped 是 Wrap 的字符串形态（测试/审计便于断言）。
func Wrapped(data any) string {
	v, _ := Wrap(data).(string)
	return v
}
