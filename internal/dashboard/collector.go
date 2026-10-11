package dashboard

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	deploymentpb "go.temporal.io/api/deployment/v1"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/director"
	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/orders"
)

const (
	buildIDLabel        = "temporal.io/build-id"
	deploymentNameLabel = "temporal.io/deployment-name"
)

type Config struct {
	Temporal             client.Client
	Namespace            string // Temporal namespace
	K8s                  kubernetes.Interface
	K8sNamespace         string
	WorkerDeploymentName string // k8s WorkerDeployment resource name
	DirectorDeployment   string // k8s Deployment name of the director
	PrometheusURL        string
	Logger               *slog.Logger
}

// Collector polls each source on its own cadence and keeps the latest values.
type Collector struct {
	cfg  Config
	http *http.Client

	mu        sync.RWMutex
	director  *director.Snapshot
	queue     QueueStats
	running   int64
	tVersions map[string]*Version // from Temporal, by Build ID
	pods      []Pod
	dirPod    *Pod
	k8sVers   map[string]*Version // from Kubernetes, by Build ID
	slots     map[string][2]float64
	health    Health
	errs      map[string]string
}

func NewCollector(cfg Config) *Collector {
	return &Collector{
		cfg:       cfg,
		http:      &http.Client{Timeout: 2 * time.Second},
		tVersions: map[string]*Version{},
		k8sVers:   map[string]*Version{},
		slots:     map[string][2]float64{},
		errs:      map[string]string{},
	}
}

func (c *Collector) temporalDeploymentName() string {
	return c.cfg.K8sNamespace + "/" + c.cfg.WorkerDeploymentName
}

func (c *Collector) Run(ctx context.Context) {
	go c.loop(ctx, time.Second, c.collectDirector)
	go c.loop(ctx, time.Second, c.collectTemporal)
	go c.loop(ctx, 2*time.Second, c.collectRunning)
	go c.loop(ctx, time.Second, c.collectK8s)
	go c.loop(ctx, 2*time.Second, c.collectPrometheus)
}

func (c *Collector) loop(ctx context.Context, every time.Duration, fn func(context.Context) error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, every+time.Second)
		err := fn(cctx)
		cancel()
		_ = err
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Collector) setErr(key string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		delete(c.errs, key)
		return
	}
	if c.errs[key] != err.Error() {
		c.cfg.Logger.Warn("collector error", "source", key, "error", err)
	}
	c.errs[key] = err.Error()
}

func (c *Collector) collectDirector(ctx context.Context) error {
	var snap director.Snapshot
	resp, err := c.cfg.Temporal.QueryWorkflow(ctx, director.WorkflowID, "", director.QueryState)
	if err == nil {
		err = resp.Get(&snap)
	}
	c.setErr("director", err)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.Director = err == nil
	if err != nil {
		c.director = nil
		return err
	}
	c.director = &snap
	return nil
}

func (c *Collector) collectTemporal(ctx context.Context) error {
	svc := c.cfg.Temporal.WorkflowService()
	ns := c.cfg.Namespace

	var q QueueStats
	for _, tqType := range []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY} {
		resp, err := svc.DescribeTaskQueue(ctx, &workflowservice.DescribeTaskQueueRequest{
			Namespace:     ns,
			TaskQueue:     &taskqueuepb.TaskQueue{Name: orders.TaskQueue, Kind: enumspb.TASK_QUEUE_KIND_NORMAL},
			TaskQueueType: tqType,
			ReportStats:   true,
		})
		if err != nil {
			c.setErr("temporal", err)
			c.mu.Lock()
			c.health.Temporal = false
			c.mu.Unlock()
			return err
		}
		addStats(&q, tqType, resp.GetStats())
	}

	versions := map[string]*Version{}
	dep, err := svc.DescribeWorkerDeployment(ctx, &workflowservice.DescribeWorkerDeploymentRequest{
		Namespace:      ns,
		DeploymentName: c.temporalDeploymentName(),
	})
	if err == nil {
		info := dep.GetWorkerDeploymentInfo()
		routing := info.GetRoutingConfig()
		for _, s := range info.GetVersionSummaries() {
			bid := s.GetDeploymentVersion().GetBuildId()
			v := &Version{
				BuildID:  bid,
				Status:   versionStatus(s.GetStatus()),
				Drainage: strings.ToLower(strings.TrimPrefix(s.GetDrainageStatus().String(), "VERSION_DRAINAGE_STATUS_")),
				Created:  s.GetCreateTime().AsTime(),
			}
			if v.Status == "ramping" {
				v.RampPercent = float64(routing.GetRampingVersionPercentage())
			}
			if v.Status == "current" && routing.GetRampingDeploymentVersion() != nil {
				v.RampPercent = 100 - float64(routing.GetRampingVersionPercentage())
			} else if v.Status == "current" {
				v.RampPercent = 100
			}
			versions[bid] = v
		}
		// Per-version backlog, so the UI can show where work is queued during a rollout.
		var perVersionTotal QueueStats
		for bid, v := range versions {
			if v.Status == "drained" || v.Status == "inactive" {
				continue
			}
			vr, err := svc.DescribeWorkerDeploymentVersion(ctx, &workflowservice.DescribeWorkerDeploymentVersionRequest{
				Namespace:            ns,
				DeploymentVersion:    &deploymentpb.WorkerDeploymentVersion{DeploymentName: c.temporalDeploymentName(), BuildId: bid},
				ReportTaskQueueStats: true,
			})
			if err != nil {
				continue
			}
			for _, tq := range vr.GetVersionTaskQueues() {
				if tq.GetName() != orders.TaskQueue {
					continue
				}
				v.Backlog += tq.GetStats().GetApproximateBacklogCount()
				addStats(&perVersionTotal, tq.GetType(), tq.GetStats())
			}
		}
		// Prefer whichever view reports more queued work; depending on server
		// version the unversioned describe may or may not include versioned backlogs.
		if perVersionTotal.Backlog > q.Backlog {
			q.WorkflowBacklog, q.ActivityBacklog, q.Backlog = perVersionTotal.WorkflowBacklog, perVersionTotal.ActivityBacklog, perVersionTotal.Backlog
			q.BacklogAgeSec = max(q.BacklogAgeSec, perVersionTotal.BacklogAgeSec)
		}
	}
	c.setErr("temporal", nil)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.Temporal = true
	c.queue = q
	c.tVersions = versions
	return nil
}

func addStats(q *QueueStats, t enumspb.TaskQueueType, s *taskqueuepb.TaskQueueStats) {
	if s == nil {
		return
	}
	n := s.GetApproximateBacklogCount()
	if t == enumspb.TASK_QUEUE_TYPE_WORKFLOW {
		q.WorkflowBacklog += n
	} else {
		q.ActivityBacklog += n
		q.AddRate += float64(s.GetTasksAddRate())
		q.DispatchRate += float64(s.GetTasksDispatchRate())
	}
	q.Backlog += n
	q.BacklogAgeSec = max(q.BacklogAgeSec, s.GetApproximateBacklogAge().AsDuration().Seconds())
}

func versionStatus(s enumspb.WorkerDeploymentVersionStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "WORKER_DEPLOYMENT_VERSION_STATUS_"))
}

func (c *Collector) collectRunning(ctx context.Context) error {
	resp, err := c.cfg.Temporal.CountWorkflow(ctx, &workflowservice.CountWorkflowExecutionsRequest{
		Namespace: c.cfg.Namespace,
		Query:     fmt.Sprintf("WorkflowType = '%s' AND ExecutionStatus = 'Running'", orders.WorkflowName),
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.running = resp.GetCount()
	c.mu.Unlock()
	return nil
}

func (c *Collector) collectK8s(ctx context.Context) error {
	if c.cfg.K8s == nil {
		return nil
	}
	ns := c.cfg.K8sNamespace
	podList, err := c.cfg.K8s.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err == nil {
		var deps []Version
		depList, derr := c.cfg.K8s.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
			LabelSelector: deploymentNameLabel + "=" + c.cfg.WorkerDeploymentName,
		})
		if derr != nil {
			err = derr
		} else {
			for _, d := range depList.Items {
				v := Version{
					BuildID: d.Labels[buildIDLabel],
					Label:   labelOf(d.Spec.Template.Annotations),
					Ready:   d.Status.ReadyReplicas,
					Created: d.CreationTimestamp.Time,
				}
				if d.Spec.Replicas != nil {
					v.Desired = *d.Spec.Replicas
				}
				deps = append(deps, v)
			}
		}
		if err == nil {
			var pods []Pod
			var dirPod *Pod
			for _, p := range podList.Items {
				pod := toPod(p)
				if p.Labels[deploymentNameLabel] == c.cfg.WorkerDeploymentName {
					pods = append(pods, pod)
				} else if p.Labels["app"] == c.cfg.DirectorDeployment && (dirPod == nil || !pod.Terminating) {
					dirPod = &pod
				}
			}
			slices.SortFunc(pods, func(a, b Pod) int { return a.Created.Compare(b.Created) })
			kv := map[string]*Version{}
			for i := range deps {
				kv[deps[i].BuildID] = &deps[i]
			}
			c.mu.Lock()
			c.pods, c.dirPod, c.k8sVers = pods, dirPod, kv
			c.health.Kubernetes = true
			c.mu.Unlock()
		}
	}
	c.setErr("kubernetes", err)
	if err != nil {
		c.mu.Lock()
		c.health.Kubernetes = false
		c.mu.Unlock()
	}
	return err
}

func labelOf(ann map[string]string) string {
	if l := ann[director.VersionAnnotation]; l != "" {
		return l
	}
	return director.VersionLabels[0]
}

func toPod(p corev1.Pod) Pod {
	ready := false
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	return Pod{
		Name:        p.Name,
		BuildID:     p.Labels[buildIDLabel],
		Label:       labelOf(p.Annotations),
		Phase:       string(p.Status.Phase),
		Ready:       ready,
		Terminating: p.DeletionTimestamp != nil,
		Created:     p.CreationTimestamp.Time,
	}
}

// collectPrometheus reads per-pod activity slot usage. These are the same SDK
// gauges KEDA's Prometheus trigger scales on.
func (c *Collector) collectPrometheus(ctx context.Context) error {
	if c.cfg.PrometheusURL == "" {
		return nil
	}
	used, err := c.promQuery(ctx, `sum by (pod) (temporal_worker_task_slots_used{worker_type="ActivityWorker"})`)
	var avail map[string]float64
	if err == nil {
		avail, err = c.promQuery(ctx, `sum by (pod) (temporal_worker_task_slots_available{worker_type="ActivityWorker"})`)
	}
	c.setErr("prometheus", err)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.Prometheus = err == nil
	if err != nil {
		return err
	}
	slots := map[string][2]float64{}
	for pod, u := range used {
		slots[pod] = [2]float64{u, u + avail[pod]}
	}
	c.slots = slots
	return nil
}

func (c *Collector) promQuery(ctx context.Context, q string) (map[string]float64, error) {
	u := strings.TrimRight(c.cfg.PrometheusURL, "/") + "/api/v1/query?query=" + url.QueryEscape(q)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: %s", body.Status)
	}
	out := map[string]float64{}
	for _, r := range body.Data.Result {
		s, _ := r.Value[1].(string)
		f, _ := strconv.ParseFloat(s, 64)
		out[r.Metric["pod"]] = f
	}
	return out, nil
}

// Snapshot merges the latest values from every source.
func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	s := Snapshot{
		Time:          time.Now(),
		Director:      c.director,
		Queue:         c.queue,
		RunningOrders: c.running,
		DirectorPod:   c.dirPod,
		Health:        c.health,
	}
	for k, e := range c.errs {
		s.Health.Errors = append(s.Health.Errors, k+": "+e)
	}
	slices.Sort(s.Health.Errors)

	byID := map[string]*Version{}
	get := func(bid string) *Version {
		if v, ok := byID[bid]; ok {
			return v
		}
		v := &Version{BuildID: bid, Status: "inactive", Label: director.VersionLabels[0]}
		byID[bid] = v
		return v
	}
	for bid, tv := range c.tVersions {
		v := get(bid)
		v.Status, v.RampPercent, v.Drainage, v.Backlog, v.Created = tv.Status, tv.RampPercent, tv.Drainage, tv.Backlog, tv.Created
	}
	for bid, kv := range c.k8sVers {
		if bid == "" {
			continue
		}
		v := get(bid)
		v.Label, v.Desired, v.Ready = kv.Label, kv.Desired, kv.Ready
		if v.Created.IsZero() {
			v.Created = kv.Created
		}
	}

	var used, total float64
	alive := 0
	for _, p := range c.pods {
		sl := c.slots[p.Name]
		p.SlotsUsed, p.SlotsTotal = sl[0], sl[1]
		s.Pods = append(s.Pods, p)
		if v := get(p.BuildID); p.BuildID != "" {
			v.SlotsUsed += sl[0]
			v.SlotsTotal += sl[1]
		}
		if !p.Terminating {
			alive++
			used += sl[0]
			total += sl[1]
		}
	}

	for _, v := range byID {
		// Hide long-gone versions that have no pods left.
		if (v.Status == "drained" || v.Status == "inactive") && v.Desired == 0 && v.Ready == 0 && !hasPods(s.Pods, v.BuildID) {
			continue
		}
		s.Versions = append(s.Versions, *v)
	}
	slices.SortFunc(s.Versions, func(a, b Version) int {
		return cmp.Or(a.Created.Compare(b.Created), strings.Compare(a.BuildID, b.BuildID))
	})

	s.Point = Point{T: s.Time.UnixMilli(), Backlog: s.Queue.Backlog, Pods: alive}
	if total > 0 {
		s.Point.Util = used / total
	}
	if s.Director != nil {
		s.Point.TargetRate = s.Director.TargetRate
	}
	return s
}

func hasPods(pods []Pod, bid string) bool {
	for _, p := range pods {
		if p.BuildID == bid {
			return true
		}
	}
	return false
}
