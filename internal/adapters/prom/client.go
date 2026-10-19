// Package prom 是 Prometheus 只读 HTTP API 适配器（M1 T1.3）。
//
// 只实现 /api/v1/query 与 /api/v1/query_range；不触碰管理/配置/写端点，
// 不做 PromQL 语义改写，不重试（重试是放大器）。所有出站请求受 ctx 超时控制。
package prom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxBodyBytes 防止上游返回巨体打爆进程；超出后 JSON 解码失败，调用方看到 downstream 错误。
const maxBodyBytes = 4 << 20

// Point 是一个样本（instant 的 value 或 range 的 values 元素）。
type Point struct {
	Timestamp time.Time
	Value     float64
}

// Series 是一条时序（metric 标签集 + 样本）。
type Series struct {
	Metric map[string]string
	Values []Point
}

// Result 是 Prometheus data 的归一化结果。
type Result struct {
	ResultType string
	Series     []Series
}

// Client 是只读 Prometheus HTTP 客户端。
type Client struct {
	base      *url.URL
	http      *http.Client
	maxSeries int
}

// New 构造客户端。baseURL 形如 http://prometheus:9090（可带 path 前缀）。
func New(baseURL string, timeout time.Duration, maxSeries int) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("非法 Prometheus URL %q", baseURL)
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if maxSeries <= 0 {
		maxSeries = 50
	}
	return &Client{
		base:      u,
		http:      &http.Client{Timeout: timeout},
		maxSeries: maxSeries,
	}, nil
}

// MaxSeries 返回适配器级序列上限（工具层还会再表达一次 truncation）。
func (c *Client) MaxSeries() int { return c.maxSeries }

// Query 执行 instant query；at 为零值时由 Prometheus 取当前时刻。
func (c *Client) Query(ctx context.Context, expr string, at time.Time) (*Result, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, errors.New("query 不能为空")
	}
	q := url.Values{}
	q.Set("query", expr)
	if !at.IsZero() {
		q.Set("time", formatUnix(at))
	}
	return c.get(ctx, "/api/v1/query", q)
}

// QueryRange 执行 range query；start/end/step 必须合法（调用方先校验，适配器再兜底）。
func (c *Client) QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) (*Result, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, errors.New("query 不能为空")
	}
	if !end.After(start) {
		return nil, errors.New("end 必须晚于 start")
	}
	if step <= 0 {
		return nil, errors.New("step 必须大于 0")
	}
	q := url.Values{}
	q.Set("query", expr)
	q.Set("start", formatUnix(start))
	q.Set("end", formatUnix(end))
	q.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))
	return c.get(ctx, "/api/v1/query_range", q)
}

func (c *Client) get(ctx context.Context, path string, q url.Values) (*Result, error) {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造 Prometheus 请求: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus 请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("读取 Prometheus 响应: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("prometheus HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw struct {
		Status    string          `json:"status"`
		Data      json.RawMessage `json:"data"`
		ErrorType string          `json:"errorType"`
		Error     string          `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("解析 Prometheus 响应: %w", err)
	}
	if raw.Status != "success" {
		return nil, fmt.Errorf("prometheus 查询失败（%s）: %s", raw.ErrorType, raw.Error)
	}
	if len(raw.Data) == 0 {
		return nil, errors.New("prometheus 响应缺 data")
	}
	var data struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string   `json:"metric"`
			Value  []json.RawMessage   `json:"value"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw.Data, &data); err != nil {
		return nil, fmt.Errorf("解析 Prometheus data: %w", err)
	}
	out := &Result{ResultType: data.ResultType}
	for _, s := range data.Result {
		ser := Series{Metric: s.Metric}
		if len(s.Value) == 2 {
			p, err := parsePoint(s.Value)
			if err != nil {
				return nil, err
			}
			ser.Values = []Point{p}
		}
		for _, pair := range s.Values {
			p, err := parsePoint(pair)
			if err != nil {
				return nil, err
			}
			ser.Values = append(ser.Values, p)
		}
		out.Series = append(out.Series, ser)
	}
	return out, nil
}

func parsePoint(pair []json.RawMessage) (Point, error) {
	if len(pair) != 2 {
		return Point{}, fmt.Errorf("prometheus 样本格式非法（期望 2 元组）")
	}
	ts, err := parseFloat(pair[0])
	if err != nil {
		return Point{}, fmt.Errorf("prometheus 时间戳: %w", err)
	}
	v, err := parseFloat(pair[1])
	if err != nil {
		return Point{}, fmt.Errorf("prometheus 样本值: %w", err)
	}
	sec := int64(ts)
	nsec := int64((ts - float64(sec)) * 1e9)
	return Point{Timestamp: time.Unix(sec, nsec).UTC(), Value: v}, nil
}

func parseFloat(raw json.RawMessage) (float64, error) {
	s := strings.TrimSpace(string(raw))
	// Prometheus JSON 里 value 是 ["123.4"] 字符串形式；也兼容裸数字。
	s = strings.Trim(s, `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("非法浮点 %q", s)
	}
	return f, nil
}

func formatUnix(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', -1, 64)
}
