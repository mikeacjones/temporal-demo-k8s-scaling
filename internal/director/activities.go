package director

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/orders"
)

// VersionAnnotation lives on the WorkerDeployment's pod template. Changing it
// changes the pod-template hash, which gives the Worker Controller a new
// Build ID and kicks off a progressive rollout. The worker reads it back via
// the downward API to display which version handled an order.
const VersionAnnotation = "demo.temporal.io/version-label"

// VersionLabels are the codenames new versions cycle through.
var VersionLabels = []string{"aurora", "nebula", "quasar", "pulsar", "comet", "nova", "zenith", "stardust"}

var WorkerDeploymentGVR = schema.GroupVersionResource{Group: "temporal.io", Version: "v1alpha1", Resource: "workerdeployments"}

type BatchRequest struct {
	Prefix string `json:"prefix"`
	First  int64  `json:"first"`
	Count  int    `json:"count"`
}

type Activities struct {
	Client client.Client
	// K8s may be nil when running outside a cluster; rollouts then fail softly.
	K8s                  dynamic.Interface
	K8sNamespace         string
	WorkerDeploymentName string
}

// StartBatch starts Count OrderWorkflows concurrently and returns how many started.
func (a *Activities) StartBatch(ctx context.Context, req BatchRequest) (int, error) {
	var started atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	var firstErr atomic.Value
	for i := range req.Count {
		wg.Add(1)
		sem <- struct{}{}
		go func(seq int64) {
			defer func() { <-sem; wg.Done() }()
			id := fmt.Sprintf("%s-%d", req.Prefix, seq)
			_, err := a.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
				ID:                       id,
				TaskQueue:                orders.TaskQueue,
				WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
			}, orders.WorkflowName, orders.Order{
				ID:       id,
				Customer: customers[rand.IntN(len(customers))],
				Items:    1 + rand.IntN(5),
				Amount:   float64(500+rand.IntN(20000)) / 100,
			})
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			started.Add(1)
		}(req.First + int64(i))
	}
	wg.Wait()
	n := int(started.Load())
	if n == 0 && req.Count > 0 {
		if err, ok := firstErr.Load().(error); ok {
			return 0, err
		}
	}
	return n, nil
}

var customers = []string{"ziggy", "ada", "grace", "linus", "margaret", "dennis", "barbara", "ken", "radia", "alan"}

// RolloutVersion bumps the version annotation on the order worker's pod
// template to the next codename. The Worker Controller does the rest.
func (a *Activities) RolloutVersion(ctx context.Context) (RolloutInfo, error) {
	if a.K8s == nil {
		return RolloutInfo{}, fmt.Errorf("no kubernetes access configured")
	}
	res := a.K8s.Resource(WorkerDeploymentGVR).Namespace(a.K8sNamespace)
	wd, err := res.Get(ctx, a.WorkerDeploymentName, metav1.GetOptions{})
	if err != nil {
		return RolloutInfo{}, fmt.Errorf("get WorkerDeployment: %w", err)
	}

	// v1.12+ uses spec.deployment.template; older manifests use spec.template.
	path := []string{"spec", "deployment", "template"}
	if _, found, _ := unstructured.NestedMap(wd.Object, path...); !found {
		path = []string{"spec", "template"}
	}
	annotations, _, _ := unstructured.NestedStringMap(wd.Object, append(path, "metadata", "annotations")...)
	from := annotations[VersionAnnotation]
	if from == "" {
		from = VersionLabels[0]
	}
	to := VersionLabels[(slices.Index(VersionLabels, from)+1)%len(VersionLabels)]

	patch := map[string]any{}
	cur := patch
	for _, p := range path {
		next := map[string]any{}
		cur[p] = next
		cur = next
	}
	cur["metadata"] = map[string]any{"annotations": map[string]string{VersionAnnotation: to}}
	body, _ := json.Marshal(patch)
	if _, err := res.Patch(ctx, a.WorkerDeploymentName, types.MergePatchType, body, metav1.PatchOptions{FieldManager: "traffic-director"}); err != nil {
		return RolloutInfo{From: from, To: to}, fmt.Errorf("patch WorkerDeployment: %w", err)
	}
	activity.GetLogger(ctx).Info("rolled out new worker version", "from", from, "to", to)
	return RolloutInfo{From: from, To: to}, nil
}
