package session

import "testing"

func TestLimitsFromEnvDefaults(t *testing.T) {
	t.Setenv("AEGIS_SESSION_MAX_TOOL_CALLS", "")
	t.Setenv("AEGIS_SESSION_MAX_TOKENS", "")
	t.Setenv("AEGIS_SESSION_QPS", "")
	t.Setenv("AEGIS_SESSION_BUDGET_DISABLED", "")
	l := LimitsFromEnv()
	if l.MaxToolCalls != 50 || l.MaxTokens != 200000 || l.QPS != 10 {
		t.Fatalf("defaults=%+v", l)
	}
	t.Setenv("AEGIS_SESSION_MAX_TOOL_CALLS", "7")
	if got := LimitsFromEnv().MaxToolCalls; got != 7 {
		t.Fatalf("override=%d", got)
	}
	t.Setenv("AEGIS_SESSION_BUDGET_DISABLED", "1")
	if got := LimitsFromEnv(); got.MaxToolCalls != 0 || got.MaxTokens != 0 {
		t.Fatalf("disabled=%+v", got)
	}
}
