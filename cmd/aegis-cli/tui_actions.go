package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

type tuiAction struct {
	kind       string
	args       string
	namespace  string
	kubeconfig string
	gateway    string
	approval   string
	actor      string
	sessionRef string
	fields     []string
	change     tuiChangeIntent
}

type tuiActionOutput struct {
	entries   []tuiEntry
	changeKey string
}

func validHTTPEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

func (t *tui) newAction(kind string) tuiAction {
	return tuiAction{
		kind: kind, namespace: t.namespace, kubeconfig: t.kubeconfig,
		gateway: t.gateway, approval: t.approval, actor: t.actor,
		sessionRef: sessionID(t), fields: []string{},
	}
}

func (a tuiAction) timeout() time.Duration {
	switch a.kind {
	case "scan":
		return 60 * time.Second
	case "change":
		return 35 * time.Second
	case "approve", "deny", "cr":
		return 15 * time.Second
	default:
		return 30 * time.Second
	}
}

func (a tuiAction) execute(ctx context.Context) tuiActionOutput {
	switch a.kind {
	case "scan":
		return a.scan(ctx)
	case "change":
		return a.changeRequest(ctx)
	case "approve", "deny":
		return a.approveCR(ctx)
	case "cr":
		return a.crStatus(ctx)
	default:
		return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleError, text: "unknown action " + a.kind}}}
	}
}

func (a tuiAction) scan(ctx context.Context) tuiActionOutput {
	ns := a.namespace
	format := ""
	fields := strings.Fields(a.args)
	for i := 0; i < len(fields); i++ {
		arg := fields[i]
		switch {
		case (arg == "-n" || arg == "--namespace") && i+1 < len(fields):
			ns = fields[i+1]
			i++
		case strings.HasPrefix(arg, "--namespace="):
			ns = strings.TrimPrefix(arg, "--namespace=")
		case arg == "json" || arg == "text" || arg == "sarif":
			format = arg
		case !strings.HasPrefix(arg, "-"):
			ns = arg
		}
	}
	cmdArgs := []string{"--kubeconfig", a.kubeconfig}
	if ns != "" {
		cmdArgs = append(cmdArgs, "--namespace", ns)
	}
	if format != "" {
		cmdArgs = append(cmdArgs, "--format", format)
	}
	var buf bytes.Buffer
	scanCmd := newScanCmd()
	scanCmd.SetContext(ctx)
	scanCmd.SetOut(&buf)
	scanCmd.SetErr(&buf)
	scanCmd.SetArgs(cmdArgs)
	if err := scanCmd.Execute(); err != nil {
		return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleError, text: "scan failed: " + err.Error()}}}
	}
	return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleAegis, text: scanDisplay(buf.String())}}}
}

func scanDisplay(out string) string {
	const maxBytes = 64 * 1024
	out = strings.TrimSpace(out)
	if out == "" {
		return "scan completed without output"
	}
	if len(out) <= maxBytes {
		return out
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(out[:cut]) {
		cut--
	}
	return out[:cut] + "\n... output truncated; use aegis-cli scan --output <path> for the full report"
}

func (a tuiAction) changeRequest(ctx context.Context) tuiActionOutput {
	change := a.change
	gw, err := newGatewayClient(a.gateway, 30*time.Second)
	if err != nil {
		return changeError(err)
	}
	out, err := gw.call(ctx, "request_change", map[string]any{
		"session_ref": a.sessionRef, "idempotency_key": change.idempotencyKey,
		"intent": "scale " + change.namespace + "/" + change.name + " to " + strconv.Itoa(change.to),
		"source": "human", "requested_by": a.actor,
		"tool": "scale_deployment", "verb": "patch", "resource": "deployments",
		"namespace": change.namespace,
		"args": map[string]any{
			"namespace": change.namespace, "name": change.name,
			"replicas": change.to, "from_replicas": change.from,
		},
		"dry_run_passed": false, "blast_radius_pods": 1,
	})
	if err != nil {
		return changeError(fmt.Errorf("request_change: %w", err))
	}
	line := fmt.Sprintf("cr=%v allow=%v requires_approval=%v reject=%v created=%v",
		out["cr_name"], out["allow"], out["requires_approval"], out["reject"], out["created"])
	entries := []tuiEntry{{role: tuiRoleAegis, text: line}}
	if out["requires_approval"] == true {
		entries = append(entries, tuiEntry{role: tuiRoleSystem, text: "approve with: /approve " + fmt.Sprint(out["cr_name"])})
	}
	return tuiActionOutput{entries: entries, changeKey: change.idempotencyKey}
}

func changeError(err error) tuiActionOutput {
	return tuiActionOutput{entries: []tuiEntry{{
		role: tuiRoleError,
		text: err.Error() + "\nThe change remains pending; /confirm retries with the same idempotency key.",
	}}}
}

func (a tuiAction) approveCR(ctx context.Context) tuiActionOutput {
	fields := a.fields
	if len(fields) < 2 {
		return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleError, text: "usage: " + fields[0] + " <cr>"}}}
	}
	cr := fields[1]
	if !strings.Contains(cr, "/") {
		cr = "kagent/" + cr
	}
	actor := a.actor
	if len(fields) >= 3 && strings.TrimSpace(fields[2]) != "" {
		actor = strings.TrimSpace(fields[2])
	}
	cmd := newApproveCmd(a.kind == "approve")
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetContext(ctx)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--endpoint", a.approval, "--cr", cr, "--actor", actor, "--note", "tui"})
	if err := cmd.Execute(); err != nil {
		return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleError, text: err.Error() + " " + strings.TrimSpace(buf.String())}}}
	}
	return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleSystem, text: strings.TrimSpace(buf.String())}}}
}

func (a tuiAction) crStatus(ctx context.Context) tuiActionOutput {
	if len(a.fields) < 2 {
		return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleError, text: "usage: /cr <cr-name>"}}}
	}
	name := strings.TrimPrefix(a.fields[1], "kagent/")
	cfg, err := clientcmd.BuildConfigFromFlags("", a.kubeconfig)
	if err != nil {
		return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleError, text: "kubeconfig: " + err.Error()}}}
	}
	cfg.Timeout = 15 * time.Second
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleError, text: "client: " + err.Error()}}}
	}
	u, err := dc.Resource(schema.GroupVersionResource{Group: "aegis.dev", Version: "v1alpha1", Resource: "changerequests"}).Namespace("kagent").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return tuiActionOutput{entries: []tuiEntry{{role: tuiRoleError, text: err.Error()}}}
	}
	state, _, _ := unstructured.NestedString(u.Object, "status", "state")
	approval, _, _ := unstructured.NestedString(u.Object, "status", "approval", "state")
	approver, _, _ := unstructured.NestedString(u.Object, "status", "approval", "approver")
	return tuiActionOutput{entries: []tuiEntry{{
		role: tuiRoleSystem,
		text: fmt.Sprintf("%s state=%s approval=%s approver=%s", name, state, approval, approver),
	}}}
}

func helpText() string {
	return `commands:
  /scan [ns] [json|text|sarif]  Tier-0 scan (requires Kubernetes, no LLM)
  /status                     show session, endpoints and request state
  /agent <url>                set the kagent A2A endpoint
  /gatekeeper [url]           show or set the gatekeeper endpoint
  /namespace [ns]             set scan namespace; empty means all
  /sessions                   list recent local sessions
  /resume <id>                resume a local transcript
  /new                        start a new local session
  /change <ns> <deploy> <from> <to>  submit an explicit scale intent
  /confirm                    confirm or retry the pending change
  /cancel                     cancel the running operation or discard the preview
  /approve <cr> [approver]    approve through the approval service
  /deny <cr> [approver]       deny through the approval service
  /cr <cr>                    read ChangeRequest status
  /help                       show this help
  /exit, /quit                exit

Keys: Enter sends; Up/Down and PgUp/PgDn scroll; Esc cancels; Ctrl+C quits.
Natural-language scale intents never submit before /confirm.`
}
