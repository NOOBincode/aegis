package k8s

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/aegis-dev/aegis/internal/authority"
)

// AuthorityWatcher 是 T2.9 归因图谱的 informer 缓存：Deployment/StatefulSet/HPA
// 的任一变更标记 dirty，下一次 Graph 读取时重建；周期 resync 兜底丢失事件。
//
// 它只读集群，不持有写权限；图谱重建失败时 Graph 返回错误（调用方 fail-closed），
// 不返回旧图谱假装新鲜。
type AuthorityWatcher struct {
	client   kubernetes.Interface
	provider *AuthorityProvider
	resync   time.Duration
	ttl      time.Duration

	mu    sync.Mutex
	graph *authority.Graph
	at    time.Time
	dirty atomic.Bool
}

// NewAuthorityWatcher 构造；ttl<=0 默认 10s，resync<=0 默认 30s。
func NewAuthorityWatcher(client kubernetes.Interface, resync, ttl time.Duration) *AuthorityWatcher {
	if resync <= 0 {
		resync = 30 * time.Second
	}
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	return &AuthorityWatcher{client: client, provider: NewAuthorityProvider(client), resync: resync, ttl: ttl}
}

// Run 启动 informer 并周期重建；阻塞到 ctx 取消或缓存同步失败。
func (w *AuthorityWatcher) Run(ctx context.Context) error {
	if w.client == nil {
		return fmt.Errorf("authority watcher client 为空")
	}
	factory := informers.NewSharedInformerFactory(w.client, w.resync)
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { w.dirty.Store(true) },
		UpdateFunc: func(_, _ any) { w.dirty.Store(true) },
		DeleteFunc: func(any) { w.dirty.Store(true) },
	}
	if _, err := factory.Apps().V1().Deployments().Informer().AddEventHandler(handler); err != nil {
		return fmt.Errorf("注册 deployment informer: %w", err)
	}
	if _, err := factory.Apps().V1().StatefulSets().Informer().AddEventHandler(handler); err != nil {
		return fmt.Errorf("注册 statefulset informer: %w", err)
	}
	if _, err := factory.Autoscaling().V2().HorizontalPodAutoscalers().Informer().AddEventHandler(handler); err != nil {
		return fmt.Errorf("注册 hpa informer: %w", err)
	}
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), factory.Apps().V1().Deployments().Informer().HasSynced,
		factory.Apps().V1().StatefulSets().Informer().HasSynced,
		factory.Autoscaling().V2().HorizontalPodAutoscalers().Informer().HasSynced) {
		return fmt.Errorf("authority informer 未完成同步")
	}
	// 首次全量建立；失败直接返回（调用方不得拿空图继续归因）。
	if err := w.Refresh(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(w.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.Refresh(ctx); err != nil {
				return err
			}
		}
	}
}

// Graph 返回当前图谱；dirty/过期时重建。重建失败返回错误，不返回旧图。
func (w *AuthorityWatcher) Graph(ctx context.Context) (*authority.Graph, error) {
	if w.client == nil {
		return nil, fmt.Errorf("authority watcher client 为空")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.graph != nil && !w.dirty.Load() && time.Since(w.at) < w.ttl {
		return w.graph, nil
	}
	if err := w.refreshLocked(ctx); err != nil {
		return nil, err
	}
	return w.graph, nil
}

// Refresh 强制重建；供启动/测试/显式失效使用。
func (w *AuthorityWatcher) Refresh(ctx context.Context) error {
	if w.client == nil {
		return fmt.Errorf("authority watcher client 为空")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.refreshLocked(ctx)
}

// Invalidate 标记 dirty；下一次 Graph 会重建。
func (w *AuthorityWatcher) Invalidate() { w.dirty.Store(true) }

func (w *AuthorityWatcher) refreshLocked(ctx context.Context) error {
	g, err := w.provider.BuildGraph(ctx, nil)
	if err != nil {
		return fmt.Errorf("重建 authority 图谱: %w", err)
	}
	w.graph = g
	w.at = time.Now()
	w.dirty.Store(false)
	return nil
}
