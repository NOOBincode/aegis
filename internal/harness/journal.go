// Package harness 是 aegis-harness 的纯领域可靠性组件（H1）。
//
// 当前只实现“只追加、每行 fsync、checksum hash chain”的意图日志写读；
// 它不是恢复权威（恢复读 CRD spec/status），也不记录推理流。
// 设计见 docs/harness-reliability.md。
package harness

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// EventType 是六类事件全集；未知类型 fail-closed。
type EventType string

// 六类事件（不记录 LLM 推理流）。
const (
	EventInput        EventType = "Input"
	EventIntent       EventType = "Intent"
	EventApproval     EventType = "Approval"
	EventOpTransition EventType = "OpTransition"
	EventCheckpoint   EventType = "Checkpoint"
	EventResult       EventType = "Result"
)

// Event 是一行 journal 记录。Payload 必须是已 sanitize 的结构化内容。
type Event struct {
	V         int             `json:"v"`
	Seq       uint64          `json:"seq"`
	Type      EventType       `json:"type"`
	TS        time.Time       `json:"ts"`
	CR        string          `json:"cr,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	From      string          `json:"from,omitempty"`
	To        string          `json:"to,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Checksum  string          `json:"checksum"`
}

// Journal 是单写者 append-only 文件；调用方需保证单写者。
type Journal struct {
	mu   sync.Mutex
	f    *os.File
	seq  uint64
	last string
}

// Open 打开/创建 journal。已存在时先 Verify 全链，损坏即失败，不截断静默继续。
func Open(path string) (*Journal, error) {
	if path == "" {
		return nil, fmt.Errorf("journal 路径为空")
	}
	j := &Journal{}
	if _, err := os.Stat(path); err == nil {
		f, err := os.Open(path) //nolint:gosec // 运维显式 journal 路径
		if err != nil {
			return nil, fmt.Errorf("打开已有 journal: %w", err)
		}
		events, err := Verify(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("已有 journal 校验失败: %w", err)
		}
		if len(events) > 0 {
			j.seq = events[len(events)-1].Seq
			j.last = events[len(events)-1].Checksum
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat journal: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 运维显式 journal 路径
	if err != nil {
		return nil, fmt.Errorf("打开 journal: %w", err)
	}
	j.f = f
	return j, nil
}

// Close 关闭文件。
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

// Append 追加一条事件：校验类型、递增 seq、计算 hash chain、写一行并 fsync。
// fsync 失败返回错误；调用方必须视同事件未持久化，不得执行副作用。
func (j *Journal) Append(ev Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return fmt.Errorf("journal 已关闭")
	}
	if !ev.Type.valid() {
		return fmt.Errorf("未知 journal 事件类型 %q", ev.Type)
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	ev.V = 1
	ev.Seq = j.seq + 1
	ev.Checksum = checksum(j.last, ev)
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("序列化 journal 事件: %w", err)
	}
	line = append(line, '\n')
	if _, err := j.f.Write(line); err != nil {
		return fmt.Errorf("写 journal: %w", err)
	}
	if err := j.f.Sync(); err != nil {
		return fmt.Errorf("fsync journal: %w", err)
	}
	j.seq = ev.Seq
	j.last = ev.Checksum
	return nil
}

func (t EventType) valid() bool {
	switch t {
	case EventInput, EventIntent, EventApproval, EventOpTransition, EventCheckpoint, EventResult:
		return true
	}
	return false
}

func checksum(prev string, ev Event) string {
	ev.Checksum = ""
	b, _ := json.Marshal(ev)
	h := sha256.New()
	_, _ = h.Write([]byte(prev))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(b)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Verify 逐行校验 journal：版本、seq 连续、checksum chain、类型合法。
// 返回完整事件序列；任何损坏都返回错误（调用方不得把部分结果当完整权威）。
func Verify(r io.Reader) ([]Event, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var events []Event
	var prev string
	var want uint64 = 1
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, fmt.Errorf("journal 第 %d 行 JSON 损坏: %w", want, err)
		}
		if ev.V != 1 {
			return nil, fmt.Errorf("journal 第 %d 行版本 %d 不支持", want, ev.V)
		}
		if ev.Seq != want {
			return nil, fmt.Errorf("journal seq 不连续：want=%d got=%d", want, ev.Seq)
		}
		if !ev.Type.valid() {
			return nil, fmt.Errorf("journal 第 %d 行类型非法 %q", want, ev.Type)
		}
		got := ev.Checksum
		wantSum := checksum(prev, ev)
		if got != wantSum {
			return nil, fmt.Errorf("journal 第 %d 行 checksum 不匹配", want)
		}
		prev = got
		events = append(events, ev)
		want++
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读 journal: %w", err)
	}
	return events, nil
}
