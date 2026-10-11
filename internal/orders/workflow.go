// Package orders contains the workload that gets autoscaled: a small
// order-processing saga whose activities simulate real work by sleeping.
package orders

import (
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue    = "orders"
	WorkflowName = "OrderWorkflow"
)

type Order struct {
	ID       string  `json:"id"`
	Customer string  `json:"customer"`
	Items    int     `json:"items"`
	Amount   float64 `json:"amount"`
}

type Result struct {
	OrderID      string `json:"orderId"`
	Status       string `json:"status"` // "shipped" or "refunded"
	TrackingID   string `json:"trackingId,omitempty"`
	VersionLabel string `json:"versionLabel"`
}

// OrderWorkflow validates, reserves, charges, ships, and notifies. If shipping
// fails permanently, the charge is refunded and inventory released.
//
// It is registered as Pinned: an order always finishes on the worker version
// it started on, which is what lets old versions drain during a rollout.
func OrderWorkflow(ctx workflow.Context, order Order) (Result, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        500 * time.Millisecond,
			BackoffCoefficient:     2,
			MaximumInterval:        5 * time.Second,
			MaximumAttempts:        5,
			NonRetryableErrorTypes: []string{ErrOutOfStock},
		},
	})
	var a *Activities
	res := Result{OrderID: order.ID}

	if err := workflow.ExecuteActivity(ctx, a.ValidateOrder, order).Get(ctx, nil); err != nil {
		return res, err
	}
	if err := workflow.ExecuteActivity(ctx, a.ReserveInventory, order).Get(ctx, nil); err != nil {
		return res, err
	}
	if err := workflow.ExecuteActivity(ctx, a.ChargePayment, order).Get(ctx, nil); err != nil {
		_ = workflow.ExecuteActivity(ctx, a.ReleaseInventory, order).Get(ctx, nil)
		return res, err
	}

	var tracking string
	if err := workflow.ExecuteActivity(ctx, a.ShipOrder, order).Get(ctx, &tracking); err != nil {
		var appErr *temporal.ApplicationError
		if !errors.As(err, &appErr) {
			return res, err
		}
		// Compensate: refund and put the stock back.
		_ = workflow.ExecuteActivity(ctx, a.RefundPayment, order).Get(ctx, nil)
		_ = workflow.ExecuteActivity(ctx, a.ReleaseInventory, order).Get(ctx, nil)
		res.Status = "refunded"
	} else {
		res.Status = "shipped"
		res.TrackingID = tracking
	}

	var label string
	if err := workflow.ExecuteActivity(ctx, a.NotifyCustomer, order).Get(ctx, &label); err != nil {
		return res, err
	}
	res.VersionLabel = label
	return res, nil
}
