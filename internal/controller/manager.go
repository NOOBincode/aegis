package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// Manager 是 controller-lite 的 informer 循环（M2 T2.1）。
// 它不是 controller-runtime：不提供 leader election/webhook；单进程单写者，
// 崩溃后由 informer 全量重建 + Reconciler 核对 CR 状态恢复（I10）。
// 后续替换为 controller-runtime 时，Reconciler 的业务语义不变。
type Manager struct {
	Client     dynamic.Interface
	Reconciler *Reconciler
	Resync     time.Duration
	Logger     *slog.Logger
}

// Run 启动 informer + 单 worker 队列；ctx 取消后退出。
func (m *Manager) Run(ctx context.Context) error {
	if m.Client == nil {
		return fmt.Errorf("manager client 为空")
	}
	if m.Reconciler == nil {
		m.Reconciler = NewReconciler(m.Client)
	}
	if m.Resync <= 0 {
		m.Resync = 30 * time.Second
	}
	logger := m.Logger
	if logger == nil {
		logger = slog.Default()
	}

	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(m.Client, m.Resync, metav1.NamespaceAll, nil)
	informer := factory.ForResource(DefaultGVR).Informer()
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()

	enqueue := func(obj any) {
		key, err := cache.MetaNamespaceKeyFunc(obj)
		if err != nil {
			logger.Warn("controller.enqueue_fail", "error", err)
			return
		}
		queue.Add(key)
	}
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    enqueue,
		UpdateFunc: func(_, newObj any) { enqueue(newObj) },
		DeleteFunc: enqueue,
	}); err != nil {
		return fmt.Errorf("注册 informer handler: %w", err)
	}

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("informer 未完成同步")
	}
	logger.Info("controller.started", "resync", m.Resync.String())

	for {
		item, shutdown := queue.Get()
		if shutdown {
			return nil
		}
		key := item
		ns, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			queue.Forget(item)
			queue.Done(item)
			continue
		}
		if err := m.Reconciler.Reconcile(ctx, ns, name); err != nil {
			logger.Warn("controller.reconcile_retry", "key", key, "error", err)
			queue.AddRateLimited(key)
		} else {
			queue.Forget(key)
		}
		queue.Done(item)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}
