# scan 并发与确定性约定

> 规则层在容器规模庞大时并行判定，但必须并发安全、任务互不干扰、输出确定性。

## 1. 原则：无共享状态 + 槽位隔离 + 确定性排序

规则引擎是纯 CPU 判定（不触 apiserver），并行只为缩短大集群判定延迟。三条铁律：

1. **Engine 无状态**：跨调用共享的 Engine 只持有装配期固定的只读字段（detectors/workers/now）；每次调用的可变输入（Snapshot、policies）一律经**参数**传入。
2. **槽位隔离零共享写**：`pmap` 每个 goroutine 只写自己的结果槽位 `out[i]`（按索引隔离），不加锁、无 data race。
3. **确定性输出**：收集后统一排序（severity 降序 + ID 升序），结果与并行度无关。

## 2. 签名

```go
// pmap 有界并行 map：结果按输入索引回收。fn 必须是纯函数（无共享写）。
func pmap[In, Out any](workers int, in []In, fn func(In) []Out) [][]Out

// A 层按 Pod 并行；B 层按 Deployment 并行。
func Run(detectors []Detector, pods []PodFacts, workers int) []Finding
func EvaluatePolicies(rules []PolicyRule, s Snapshot, workers int) []Finding
```

- `defaultScanWorkers = 8`（对齐 kubeagent；纯 CPU 判定，worker 上限防过度调度，非 QPS 限流）。
- `workers<=0` 或超过输入长度 → 钳制到 `[1, len(in)]`。
- 事件预索引 `indexEventsByPod`：`O(events)` 建表 + `O(1)` 取用，替代 `O(pods×events)` 嵌套全扫。

## 3. 热修改 fail-safe（B 层规则）

- `PolicySource.Load()` 每次 Scan 前调用（重读磁盘 → 热修改生效）。
- 加载/校验失败 → `service` 回退到上一份已知良好规则集（`lastGood`），并把告警记入盲点（`policy-rules:<err>`）；绝不带病运行、绝不因一条坏规则停摆。
- 首次失败且无良好集 → 空规则集 + 报错（记盲点）。
- `lastGood`/`lastGoodErr` 读写全在 `sync.RWMutex` 保护内。

## 4. Wrong vs Correct

#### Wrong（pmap 里往共享 slice append）
```go
var out []Finding
var wg sync.WaitGroup
for _, p := range pods {
    wg.Add(1)
    go func(p PodFacts) {
        defer wg.Done()
        out = append(out, detect(p)...) // data race + 顺序不确定！
    }(p)
}
```

#### Correct（每 goroutine 写自己的槽位，收集后统一排序）
```go
per := pmap(workers, pods, func(f PodFacts) []Finding { return detect(f) })
out := flatten(per)              // 按输入索引拼接
sort.SliceStable(out, byID)      // 确定性排序，与并行度无关
```

#### Wrong（事件逐 Pod 全扫）
```go
for _, p := range pods { facts.Events = filter(events, p) }  // O(pods×events)
```

#### Correct（预索引一次）
```go
byPod := indexEventsByPod(events) // O(events)
for _, p := range pods { facts.Events = byPod[podKey(p.Namespace, p.Name)] } // O(1)
```

## 5. 测试点（必须）

- **并发安全**：同一 Engine 并发 `Scan`（≥16 goroutine），结果互不干扰；`-race` 在 CI 跑（本机需 gcc）。
- **确定性**：同一输入在 workers=1/8/64 下输出逐条一致（`concurrency_test.go`）。
- **槽位隔离**：`pmap` 输出槽位数==输入数、值按索引归位、空输入返回 nil。
- **热修改**：改规则新一轮 Scan 生效；坏规则回退 lastGood 并记盲点；首次失败空规则+报错。

## 6. 采集层并发（internal/adapters/k8s/scanprovider.go）

- 4 类对象（pods/deployments/nodes/events）有界并发拉取，各写各的结果槽位。
- 单类失败分级：Forbidden/NotFound → 记盲点继续；连接级错误 → 整体返回 error（取数失败 ≠ 无异常）。
- 事件时间窗 `SinceSeconds` 过滤（注入时钟）：SystemOOM/探针佐证不取陈旧事件。
