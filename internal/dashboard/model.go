// Package dashboard collects live state from Temporal, Kubernetes, and
// Prometheus and streams it to the browser over Server-Sent Events.
package dashboard

import (
	"time"

	"github.com/mikeacjones/temporal-k8s-scaling-demo/internal/director"
)

// Snapshot is pushed to every browser once per second.
type Snapshot struct {
	Time          time.Time          `json:"time"`
	Director      *director.Snapshot `json:"director,omitempty"`
	Queue         QueueStats         `json:"queue"`
	RunningOrders int64              `json:"runningOrders"`
	Versions      []Version          `json:"versions"`
	Pods          []Pod              `json:"pods"`
	DirectorPod   *Pod               `json:"directorPod,omitempty"`
	Health        Health             `json:"health"`
	Point         Point              `json:"point"`
}

type Health struct {
	Temporal   bool     `json:"temporal"`
	Kubernetes bool     `json:"kubernetes"`
	Prometheus bool     `json:"prometheus"`
	Director   bool     `json:"director"`
	Errors     []string `json:"errors,omitempty"`
}

type QueueStats struct {
	WorkflowBacklog int64   `json:"workflowBacklog"`
	ActivityBacklog int64   `json:"activityBacklog"`
	Backlog         int64   `json:"backlog"`
	BacklogAgeSec   float64 `json:"backlogAgeSec"`
	AddRate         float64 `json:"addRate"`
	DispatchRate    float64 `json:"dispatchRate"`
}

// Version merges what Temporal knows (routing status) with what Kubernetes
// knows (replicas) for one Build ID.
type Version struct {
	BuildID     string    `json:"buildId"`
	Label       string    `json:"label"`
	Status      string    `json:"status"` // current | ramping | draining | drained | inactive
	RampPercent float64   `json:"rampPercent"`
	Drainage    string    `json:"drainage,omitempty"`
	Desired     int32     `json:"desired"`
	Ready       int32     `json:"ready"`
	Backlog     int64     `json:"backlog"`
	SlotsUsed   float64   `json:"slotsUsed"`
	SlotsTotal  float64   `json:"slotsTotal"`
	Created     time.Time `json:"created"`
}

type Pod struct {
	Name        string    `json:"name"`
	BuildID     string    `json:"buildId"`
	Label       string    `json:"label"`
	Phase       string    `json:"phase"`
	Ready       bool      `json:"ready"`
	Terminating bool      `json:"terminating"`
	SlotsUsed   float64   `json:"slotsUsed"`
	SlotsTotal  float64   `json:"slotsTotal"`
	Created     time.Time `json:"created"`
}

// Point is one sample on the live chart.
type Point struct {
	T          int64   `json:"t"` // unix millis
	TargetRate float64 `json:"rate"`
	Backlog    int64   `json:"backlog"`
	Pods       int     `json:"pods"`
	Util       float64 `json:"util"` // 0..1 average activity slot utilization
}
