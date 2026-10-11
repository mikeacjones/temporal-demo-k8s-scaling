package director

import (
	"fmt"
	"math"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue    = "traffic-director"
	WorkflowID   = "traffic-director"
	WorkflowName = "TrafficDirector"

	SignalPause      = "pause"
	SignalResume     = "resume"
	SignalSurge      = "surge"
	SignalRolloutNow = "rollout-now"
	UpdateSetConfig  = "set-config"
	QueryState       = "state"

	maxStartsPerTick = 1000
)

type Surge struct {
	Rate             float64 `json:"rate"`
	RemainingSeconds float64 `json:"remainingSeconds"`
}

type RolloutInfo struct {
	From  string    `json:"from"`
	To    string    `json:"to"`
	Cycle int       `json:"cycle"`
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
}

// State is carried across Continue-As-New, so the entity survives forever
// with bounded history.
type State struct {
	Config           Config       `json:"config"`
	Cycle            int          `json:"cycle"`
	Elapsed          float64      `json:"elapsed"`
	Seq              int64        `json:"seq"`
	Carry            float64      `json:"carry"`
	TotalStarted     int64        `json:"totalStarted"`
	Paused           bool         `json:"paused"`
	Surge            Surge        `json:"surge"`
	RolloutRequested bool         `json:"rolloutRequested"`
	LastRollout      *RolloutInfo `json:"lastRollout,omitempty"`
	IDPrefix         string       `json:"idPrefix"`
}

// Snapshot is what the "state" query returns: state plus derived values the UI renders.
type Snapshot struct {
	State
	Phase               string      `json:"phase"`
	PhaseProgress       float64     `json:"phaseProgress"`
	CycleProgress       float64     `json:"cycleProgress"`
	CycleSeconds        int         `json:"cycleSeconds"`
	TargetRate          float64     `json:"targetRate"`
	Phases              []PhaseSpan `json:"phases"`
	NextRolloutInCycles int         `json:"nextRolloutInCycles"` // -1 when disabled
	RollingOut          bool        `json:"rollingOut"`
	RunID               string      `json:"runId"`
}

func (s *State) rate() (phase string, progress, rate float64) {
	phase, progress, rate = s.Config.At(s.Elapsed)
	if s.Paused {
		phase, rate = PhasePaused, 0
	}
	if s.Surge.RemainingSeconds > 0 {
		rate = math.Max(rate, s.Surge.Rate)
	}
	return phase, progress, rate
}

func (s *State) rolloutDueThisCycle() bool {
	n := s.Config.RolloutEveryNCycles
	return n > 0 && s.Cycle > 0 && s.Cycle%n == 0
}

func (s *State) nextRolloutIn() int {
	n := s.Config.RolloutEveryNCycles
	if n <= 0 {
		return -1
	}
	return (n - s.Cycle%n) % n
}

// TrafficDirectorWorkflow is the entity. Each tick it starts a batch of
// OrderWorkflows sized by the current point on the traffic curve. Every N
// cycles it asks the Worker Controller for a new worker version.
func TrafficDirectorWorkflow(ctx workflow.Context, state State) error {
	logger := workflow.GetLogger(ctx)
	if state.Config.TickSeconds == 0 {
		state.Config = DefaultConfig()
	}
	if state.IDPrefix == "" {
		state.IDPrefix = workflow.GetInfo(ctx).FirstRunID[:8]
	}
	rollingOut := false

	if err := workflow.SetQueryHandler(ctx, QueryState, func() (Snapshot, error) {
		phase, progress, rate := state.rate()
		cycle := state.Config.CycleSeconds()
		return Snapshot{
			State:               state,
			Phase:               phase,
			PhaseProgress:       progress,
			CycleProgress:       state.Elapsed / float64(cycle),
			CycleSeconds:        cycle,
			TargetRate:          rate,
			Phases:              state.Config.Phases(),
			NextRolloutInCycles: state.nextRolloutIn(),
			RollingOut:          rollingOut,
			RunID:               workflow.GetInfo(ctx).WorkflowExecution.RunID,
		}, nil
	}); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateSetConfig,
		func(ctx workflow.Context, cfg Config) (Config, error) {
			state.Config = cfg
			if state.Elapsed >= float64(cfg.CycleSeconds()) {
				state.Elapsed = 0
			}
			return cfg, nil
		},
		workflow.UpdateHandlerOptions{Validator: func(ctx workflow.Context, cfg Config) error { return cfg.Validate() }},
	); err != nil {
		return err
	}

	pauseCh := workflow.GetSignalChannel(ctx, SignalPause)
	resumeCh := workflow.GetSignalChannel(ctx, SignalResume)
	surgeCh := workflow.GetSignalChannel(ctx, SignalSurge)
	rolloutCh := workflow.GetSignalChannel(ctx, SignalRolloutNow)
	applySurge := func(s Surge) {
		if s.RemainingSeconds <= 0 {
			s.RemainingSeconds = 60
		}
		if s.Rate <= 0 {
			s.Rate = state.Config.PeakRate * 1.5
		}
		state.Surge = s
	}
	// drain applies any buffered signals; called each tick and before Continue-As-New
	// so nothing is lost across runs.
	drain := func() {
		for pauseCh.ReceiveAsync(nil) {
			state.Paused = true
		}
		for resumeCh.ReceiveAsync(nil) {
			state.Paused = false
		}
		var s Surge
		for surgeCh.ReceiveAsync(&s) {
			applySurge(s)
		}
		for rolloutCh.ReceiveAsync(nil) {
			state.RolloutRequested = true
		}
	}

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	laCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		ScheduleToCloseTimeout: 20 * time.Second,
		// Traffic is best-effort: never retry a batch, just move on to the next tick.
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	var a *Activities

	for {
		drain()

		if state.Elapsed == 0 && !state.Paused && state.rolloutDueThisCycle() {
			state.RolloutRequested = true
		}
		if state.RolloutRequested {
			state.RolloutRequested = false
			rollingOut = true
			var res RolloutInfo
			err := workflow.ExecuteActivity(actCtx, a.RolloutVersion).Get(ctx, &res)
			if err != nil {
				res.Error = err.Error()
				logger.Warn("rollout failed", "error", err)
			}
			res.Cycle, res.At = state.Cycle, workflow.Now(ctx)
			state.LastRollout = &res
			rollingOut = false
		}

		tick := float64(state.Config.TickSeconds)
		_, _, rate := state.rate()
		want := rate*tick + state.Carry
		count := int(math.Floor(want))
		state.Carry = want - float64(count)
		if count > maxStartsPerTick {
			count, state.Carry = maxStartsPerTick, 0
		}
		if count > 0 {
			var started int
			err := workflow.ExecuteLocalActivity(laCtx, a.StartBatch, BatchRequest{
				Prefix: fmt.Sprintf("order-%s-%d", state.IDPrefix, state.Cycle),
				First:  state.Seq,
				Count:  count,
			}).Get(ctx, &started)
			if err != nil {
				logger.Warn("start batch failed", "error", err)
			}
			state.Seq += int64(count)
			state.TotalStarted += int64(started)
		}

		if err := workflow.Sleep(ctx, time.Duration(state.Config.TickSeconds)*time.Second); err != nil {
			return err
		}

		if state.Surge.RemainingSeconds > 0 {
			state.Surge.RemainingSeconds = math.Max(0, state.Surge.RemainingSeconds-tick)
		}
		cycleDone := false
		if !state.Paused {
			state.Elapsed += tick
			if state.Elapsed >= float64(state.Config.CycleSeconds()) {
				state.Elapsed, state.Cycle, state.Seq = 0, state.Cycle+1, 0
				cycleDone = true
			}
		}
		if cycleDone || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
				return err
			}
			drain()
			return workflow.NewContinueAsNewError(ctx, WorkflowName, state)
		}
	}
}
