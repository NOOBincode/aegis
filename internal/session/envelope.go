package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrReplay 表示信封序号重复/回退；ErrExpired 表示时间窗外；ErrBadSignature 表示签名错。
var (
	ErrReplay       = errors.New("envelope replay or out-of-order sequence")
	ErrExpired      = errors.New("envelope outside time window")
	ErrBadSignature = errors.New("envelope signature invalid")
)

// Envelope 是 M2 T2.11 的签名信封（HMAC 版；mTLS 在传输层另行强制）。
// 它防重放/串话：会话内序号单调递增 + 时间窗 + HMAC。密钥由进程安全注入，不落日志。
type Envelope struct {
	SessionID string `json:"session_id"`
	Seq       uint64 `json:"seq"`
	IssuedAt  int64  `json:"issued_at"` // Unix 秒
	Payload   string `json:"payload"`   // base64url(payload)
	Signature string `json:"signature"`
}

// Signer 签发信封；Verifier 校验，二者共享 secret。
type Signer struct {
	secret []byte
	window time.Duration
	now    func() time.Time
}

// NewSigner 构造签名器；secret 至少 16 字节，避免弱密钥。
func NewSigner(secret []byte, window time.Duration) (*Signer, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("envelope secret 至少 16 字节")
	}
	if window <= 0 {
		window = 30 * time.Second
	}
	return &Signer{secret: append([]byte(nil), secret...), window: window, now: time.Now}, nil
}

// Seal 返回可传输的 base64url 信封。
func (s *Signer) Seal(sessionID string, seq uint64, payload []byte) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("sessionID 不能为空")
	}
	if seq == 0 {
		return "", fmt.Errorf("seq 必须从 1 开始")
	}
	env := Envelope{
		SessionID: sessionID, Seq: seq, IssuedAt: s.now().UTC().Unix(),
		Payload: base64.RawURLEncoding.EncodeToString(payload),
	}
	env.Signature = signEnv(s.secret, env)
	b, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Verifier 校验签名/时间窗/单调序号；每个会话独立。
type Verifier struct {
	mu      sync.Mutex
	secret  []byte
	window  time.Duration
	lastSeq map[string]uint64
	now     func() time.Time
}

// NewVerifier 构造校验器。
func NewVerifier(secret []byte, window time.Duration) (*Verifier, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("envelope secret 至少 16 字节")
	}
	if window <= 0 {
		window = 30 * time.Second
	}
	return &Verifier{secret: append([]byte(nil), secret...), window: window, lastSeq: make(map[string]uint64), now: time.Now}, nil
}

// Open 校验并返回 payload；重放/序号回退/过期/签名错都拒绝。
func (v *Verifier) Open(token string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("解码信封: %w", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析信封: %w", err)
	}
	if env.SessionID == "" || env.Seq == 0 {
		return nil, fmt.Errorf("信封缺 session/seq")
	}
	if !hmac.Equal([]byte(env.Signature), []byte(signEnv(v.secret, env))) {
		return nil, ErrBadSignature
	}
	now := v.now().UTC()
	issued := time.Unix(env.IssuedAt, 0).UTC()
	if issued.Before(now.Add(-v.window)) || issued.After(now.Add(v.window)) {
		return nil, ErrExpired
	}
	v.mu.Lock()
	last := v.lastSeq[env.SessionID]
	if env.Seq <= last {
		v.mu.Unlock()
		return nil, ErrReplay
	}
	v.lastSeq[env.SessionID] = env.Seq
	v.mu.Unlock()
	payload, err := base64.RawURLEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("解码 payload: %w", err)
	}
	return payload, nil
}

func signEnv(secret []byte, env Envelope) string {
	mac := hmac.New(sha256.New, secret)
	// canonical：session|seq|issued_at|payload；不含 signature 自身。
	_, _ = fmt.Fprintf(mac, "%s|%d|%d|%s", env.SessionID, env.Seq, env.IssuedAt, env.Payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
