package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestEnvelopeSealOpen(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	s, _ := NewSigner(secret, time.Minute)
	v, _ := NewVerifier(secret, time.Minute)
	token, err := s.Seal("sess-1", 1, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Open(token)
	if err != nil || !bytes.Equal(got, []byte("payload")) {
		t.Fatalf("open: %q err=%v", got, err)
	}
}

func TestEnvelopeReplayAndOutOfOrder(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	s, _ := NewSigner(secret, time.Minute)
	v, _ := NewVerifier(secret, time.Minute)
	t1, _ := s.Seal("s", 2, []byte("x"))
	if _, err := v.Open(t1); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(t1); err != ErrReplay {
		t.Fatalf("重放应拒绝：%v", err)
	}
	tOld, _ := s.Seal("s", 1, []byte("x"))
	if _, err := v.Open(tOld); err != ErrReplay {
		t.Fatalf("序号回退应拒绝：%v", err)
	}
}

func TestEnvelopeTamperAndExpiry(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	s, _ := NewSigner(secret, time.Minute)
	v, _ := NewVerifier(secret, time.Minute)
	token, _ := s.Seal("s", 1, []byte("x"))
	// 篡改 payload：解码→改字段→重编码，确保签名不匹配。
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	env.Payload = base64.RawURLEncoding.EncodeToString([]byte("tampered"))
	raw, _ = json.Marshal(env)
	tampered := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := v.Open(tampered); err == nil {
		t.Fatal("篡改应失败")
	}
	// 时间窗：手工构造过期信封（用同一 secret 无法让 Signer 时间回退，改用 Verifier 的 now）
	oldSigner := &Signer{secret: secret, window: time.Minute, now: func() time.Time { return time.Now().Add(-2 * time.Minute) }}
	oldToken, _ := oldSigner.Seal("s2", 1, []byte("x"))
	if _, err := v.Open(oldToken); err != ErrExpired {
		t.Fatalf("过期应拒绝：%v", err)
	}
}
