package director

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestProfileShape(t *testing.T) {
	c := DefaultConfig()
	require.Equal(t, 300, c.CycleSeconds())

	phase, _, rate := c.At(0)
	require.Equal(t, PhaseIdle, phase)
	require.Equal(t, c.BaseRate, rate)

	phase, _, rate = c.At(30 + 45) // halfway up the ramp
	require.Equal(t, PhaseRampUp, phase)
	require.InDelta(t, (c.BaseRate+c.PeakRate)/2, rate, 0.01)

	phase, _, rate = c.At(120 + 1)
	require.Equal(t, PhasePeak, phase)
	require.InDelta(t, c.PeakRate, rate, c.PeakRate*0.1)

	phase, _, rate = c.At(299)
	require.Equal(t, PhaseCooldown, phase)
	require.Equal(t, c.BaseRate, rate)

	// Rate never exceeds peak by more than the wobble or drops below base.
	for e := 0.0; e < 300; e += 0.5 {
		_, _, r := c.At(e)
		require.GreaterOrEqual(t, r, c.BaseRate-1e-9)
		require.LessOrEqual(t, r, c.PeakRate*1.081)
	}
}

func TestConfigValidate(t *testing.T) {
	require.NoError(t, DefaultConfig().Validate())
	bad := DefaultConfig()
	bad.PeakRate = 0
	require.Error(t, bad.Validate())
	bad = DefaultConfig()
	bad.TickSeconds = 0
	require.Error(t, bad.Validate())
}

func TestNextRolloutIn(t *testing.T) {
	s := State{Config: DefaultConfig()}
	require.Equal(t, 3, s.nextRolloutIn())
	s.Cycle = 1
	require.Equal(t, 2, s.nextRolloutIn())
	s.Cycle = 3
	require.Equal(t, 0, s.nextRolloutIn())
	require.True(t, s.rolloutDueThisCycle())
	s.Config.RolloutEveryNCycles = 0
	require.Equal(t, -1, s.nextRolloutIn())
}

func shortConfig() Config {
	c := DefaultConfig()
	c.IdleSeconds, c.RampUpSeconds, c.PeakSeconds, c.RampDownSeconds, c.CooldownSeconds = 10, 10, 10, 10, 10
	c.BaseRate, c.PeakRate = 1, 5
	return c
}

func TestWorkflowRunsCycleAndContinuesAsNew(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(TrafficDirectorWorkflow, workflow.RegisterOptions{Name: WorkflowName})
	var a *Activities
	env.RegisterActivity(a)

	var requested int
	env.OnActivity(a.StartBatch, mock.Anything, mock.Anything).Return(
		func(_ context.Context, req BatchRequest) (int, error) { requested += req.Count; return req.Count, nil })

	env.ExecuteWorkflow(TrafficDirectorWorkflow, State{Config: shortConfig()})

	require.True(t, env.IsWorkflowCompleted())
	var can *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &can), "expected continue-as-new, got %v", env.GetWorkflowError())

	// Expected orders ≈ integral of the rate curve over one cycle.
	c := shortConfig()
	want := 0.0
	for e := 0.0; e < float64(c.CycleSeconds()); e += float64(c.TickSeconds) {
		_, _, r := c.At(e)
		want += r * float64(c.TickSeconds)
	}
	require.InDelta(t, want, float64(requested), 2)
}

func TestWorkflowPauseAndSurge(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(TrafficDirectorWorkflow, workflow.RegisterOptions{Name: WorkflowName})
	var a *Activities
	env.RegisterActivity(a)
	env.OnActivity(a.StartBatch, mock.Anything, mock.Anything).Return(
		func(_ context.Context, req BatchRequest) (int, error) { return req.Count, nil })

	query := func() Snapshot {
		v, err := env.QueryWorkflow(QueryState)
		require.NoError(t, err)
		var s Snapshot
		require.NoError(t, v.Get(&s))
		return s
	}

	env.RegisterDelayedCallback(func() { env.SignalWorkflow(SignalPause, nil) }, 5*time.Second)
	env.RegisterDelayedCallback(func() {
		s := query()
		require.True(t, s.Paused)
		require.Equal(t, PhasePaused, s.Phase)
		require.Zero(t, s.TargetRate)
		env.SignalWorkflow(SignalSurge, Surge{Rate: 42, RemainingSeconds: 10})
	}, 20*time.Second)
	env.RegisterDelayedCallback(func() {
		s := query()
		require.Equal(t, 42.0, s.TargetRate)
		env.SignalWorkflow(SignalResume, nil)
	}, 25*time.Second)
	env.RegisterDelayedCallback(func() {
		s := query()
		require.False(t, s.Paused)
		require.Zero(t, s.Surge.RemainingSeconds)
		require.False(t, math.IsNaN(s.CycleProgress))
	}, 45*time.Second)

	env.ExecuteWorkflow(TrafficDirectorWorkflow, State{Config: shortConfig()})
	var can *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &can))
}

func TestWorkflowRollsOutOnSchedule(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(TrafficDirectorWorkflow, workflow.RegisterOptions{Name: WorkflowName})
	var a *Activities
	env.RegisterActivity(a)
	env.OnActivity(a.StartBatch, mock.Anything, mock.Anything).Return(1, nil)
	env.OnActivity(a.RolloutVersion, mock.Anything).Return(RolloutInfo{From: "aurora", To: "nebula"}, nil).Once()

	env.ExecuteWorkflow(TrafficDirectorWorkflow, State{Config: shortConfig(), Cycle: 3})
	env.AssertExpectations(t)

	var can *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &can))
}

func TestWorkflowUpdateValidatesConfig(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(TrafficDirectorWorkflow, workflow.RegisterOptions{Name: WorkflowName})
	var a *Activities
	env.RegisterActivity(a)
	env.OnActivity(a.StartBatch, mock.Anything, mock.Anything).Return(1, nil)

	env.RegisterDelayedCallback(func() {
		bad := shortConfig()
		bad.TickSeconds = 0
		env.UpdateWorkflow(UpdateSetConfig, "bad", &testsuite.TestUpdateCallback{
			OnReject:   func(err error) {},
			OnAccept:   func() { t.Fatal("invalid config accepted") },
			OnComplete: func(any, error) {},
		}, bad)
		good := shortConfig()
		good.PeakRate = 7
		env.UpdateWorkflow(UpdateSetConfig, "good", &testsuite.TestUpdateCallback{
			OnReject:   func(err error) { t.Fatalf("valid config rejected: %v", err) },
			OnAccept:   func() {},
			OnComplete: func(any, error) {},
		}, good)
	}, 3*time.Second)
	env.RegisterDelayedCallback(func() {
		v, err := env.QueryWorkflow(QueryState)
		require.NoError(t, err)
		var s Snapshot
		require.NoError(t, v.Get(&s))
		require.Equal(t, 7.0, s.Config.PeakRate)
	}, 6*time.Second)

	env.ExecuteWorkflow(TrafficDirectorWorkflow, State{Config: shortConfig()})
}
