package session

import (
	"os"
	"strconv"
	"time"
)

// LimitsFromEnv 是进程启动期的配置适配器（非领域判定）。默认值即生产安全默认：
// 单会话 50 次工具调用 / 200k token 估算 / 10 QPS；可用环境变量覆盖，0=不限制。
// 这些是 M1 的临时预算源；M2 收敛进 GuardrailPolicy（gatekeeper 下发），不把
// 预算长期留在环境变量里。
func LimitsFromEnv() Limits {
	l := Limits{MaxToolCalls: 50, MaxTokens: 200_000, QPS: 10, QPSWindow: time.Second}
	if os.Getenv("AEGIS_SESSION_BUDGET_DISABLED") == "1" {
		return Limits{MaxToolCalls: 0, MaxTokens: 0, QPS: 0, QPSWindow: time.Second}
	}
	if v, ok := envInt("AEGIS_SESSION_MAX_TOOL_CALLS"); ok {
		l.MaxToolCalls = v
	}
	if v, ok := envInt("AEGIS_SESSION_MAX_TOKENS"); ok {
		l.MaxTokens = v
	}
	if v, ok := envInt("AEGIS_SESSION_QPS"); ok {
		l.QPS = v
	}
	if v, ok := envInt("AEGIS_SESSION_QPS_WINDOW_MS"); ok {
		l.QPSWindow = time.Duration(v) * time.Millisecond
	}
	return l
}

func envInt(key string) (int, bool) {
	s := os.Getenv(key)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
