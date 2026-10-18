package k8s

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/aegis-dev/aegis/internal/scan"
)

// 触顶截断必须进 PartialReads：单页采集不是全量，静默当"无异常"会造成假阴性。
func TestCollectTruncationBlindSpot(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{ListMeta: metav1.ListMeta{Continue: "opaque-next-page"}}, nil
	})

	got, err := NewScanSnapshotProvider(cs).Collect(context.Background(), scan.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	joined := strings.Join(got.PartialReads, ",")
	if !strings.Contains(joined, "pods:truncated@limit=200") {
		t.Fatalf("pods 触顶应写入盲点，got %v", got.PartialReads)
	}
	if strings.Contains(joined, "opaque-next-page") {
		t.Fatalf("盲点不应泄漏 opaque continue token：%v", got.PartialReads)
	}
}

// 未触顶时不应产生截断盲点。
func TestCollectNoTruncationBlindSpot(t *testing.T) {
	cs := fake.NewSimpleClientset()
	got, err := NewScanSnapshotProvider(cs).Collect(context.Background(), scan.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got.PartialReads) != 0 {
		t.Fatalf("空集群不应有盲点，got %v", got.PartialReads)
	}
}
