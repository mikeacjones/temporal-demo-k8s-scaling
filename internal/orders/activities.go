package orders

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"go.temporal.io/sdk/temporal"
)

const ErrOutOfStock = "OutOfStock"

// Activities simulate work with sleeps. Speed scales every duration so the
// per-pod throughput can be tuned without rebuilding (SPEED=2 halves them).
type Activities struct {
	VersionLabel string
	Speed        float64
	// FailureRate is the chance of a transient (retryable) failure per call.
	FailureRate float64
}

func (a *Activities) work(ctx context.Context, minMs, maxMs int) error {
	speed := a.Speed
	if speed <= 0 {
		speed = 1
	}
	d := time.Duration(float64(minMs+rand.IntN(maxMs-minMs+1))/speed) * time.Millisecond
	select {
	case <-time.After(d):
	case <-ctx.Done():
		return ctx.Err()
	}
	if rand.Float64() < a.FailureRate {
		return fmt.Errorf("transient downstream error")
	}
	return nil
}

func (a *Activities) ValidateOrder(ctx context.Context, o Order) error {
	return a.work(ctx, 150, 350)
}

func (a *Activities) ReserveInventory(ctx context.Context, o Order) error {
	return a.work(ctx, 300, 700)
}

func (a *Activities) ChargePayment(ctx context.Context, o Order) error {
	return a.work(ctx, 800, 1600)
}

func (a *Activities) ShipOrder(ctx context.Context, o Order) (string, error) {
	if err := a.work(ctx, 300, 700); err != nil {
		return "", err
	}
	if rand.Float64() < 0.02 {
		return "", temporal.NewNonRetryableApplicationError("warehouse out of stock", ErrOutOfStock, nil)
	}
	return fmt.Sprintf("ZIG-%06d", rand.IntN(1_000_000)), nil
}

func (a *Activities) NotifyCustomer(ctx context.Context, o Order) (string, error) {
	return a.VersionLabel, a.work(ctx, 100, 300)
}

func (a *Activities) RefundPayment(ctx context.Context, o Order) error {
	return a.work(ctx, 300, 600)
}

func (a *Activities) ReleaseInventory(ctx context.Context, o Order) error {
	return a.work(ctx, 100, 300)
}
