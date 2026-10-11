package orders

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func TestOrderShips(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	a := &Activities{VersionLabel: "aurora", Speed: 1000}
	env.RegisterActivity(a)
	env.OnActivity(a.ShipOrder, mock.Anything, mock.Anything).Return("ZIG-1", nil)

	env.ExecuteWorkflow(OrderWorkflow, Order{ID: "o1"})
	require.NoError(t, env.GetWorkflowError())
	var res Result
	require.NoError(t, env.GetWorkflowResult(&res))
	require.Equal(t, "shipped", res.Status)
	require.Equal(t, "ZIG-1", res.TrackingID)
	require.Equal(t, "aurora", res.VersionLabel)
}

func TestOrderOutOfStockCompensates(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	a := &Activities{VersionLabel: "nebula", Speed: 1000}
	env.RegisterActivity(a)
	env.OnActivity(a.ShipOrder, mock.Anything, mock.Anything).Return("",
		temporal.NewNonRetryableApplicationError("out of stock", ErrOutOfStock, nil))
	env.OnActivity(a.RefundPayment, mock.Anything, mock.Anything).Return(nil).Once()
	env.OnActivity(a.ReleaseInventory, mock.Anything, mock.Anything).Return(nil).Once()

	env.ExecuteWorkflow(OrderWorkflow, Order{ID: "o2"})
	require.NoError(t, env.GetWorkflowError())
	var res Result
	require.NoError(t, env.GetWorkflowResult(&res))
	require.Equal(t, "refunded", res.Status)
	env.AssertExpectations(t)
}
