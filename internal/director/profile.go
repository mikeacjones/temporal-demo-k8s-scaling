// Package director implements the traffic director: a long-running entity
// workflow that generates cyclical order traffic and periodically rolls out
// new worker versions, so the demo can run unattended.
package director

import (
	"errors"
	"math"
)

const (
	PhaseIdle     = "idle"
	PhaseRampUp   = "ramp-up"
	PhasePeak     = "peak"
	PhaseRampDown = "ramp-down"
	PhaseCooldown = "cooldown"
	PhasePaused   = "paused"
)

// Config controls the shape of one traffic cycle. Rates are orders/second.
type Config struct {
	IdleSeconds         int     `json:"idleSeconds"`
	RampUpSeconds       int     `json:"rampUpSeconds"`
	PeakSeconds         int     `json:"peakSeconds"`
	RampDownSeconds     int     `json:"rampDownSeconds"`
	CooldownSeconds     int     `json:"cooldownSeconds"`
	BaseRate            float64 `json:"baseRate"`
	PeakRate            float64 `json:"peakRate"`
	TickSeconds         int     `json:"tickSeconds"`
	RolloutEveryNCycles int     `json:"rolloutEveryNCycles"` // 0 disables automatic rollouts
}

func DefaultConfig() Config {
	return Config{
		IdleSeconds:         30,
		RampUpSeconds:       90,
		PeakSeconds:         60,
		RampDownSeconds:     90,
		CooldownSeconds:     30,
		BaseRate:            0.6,
		PeakRate:            10,
		TickSeconds:         2,
		RolloutEveryNCycles: 3,
	}
}

type PhaseSpan struct {
	Name    string `json:"name"`
	Seconds int    `json:"seconds"`
}

func (c Config) Phases() []PhaseSpan {
	return []PhaseSpan{
		{PhaseIdle, c.IdleSeconds},
		{PhaseRampUp, c.RampUpSeconds},
		{PhasePeak, c.PeakSeconds},
		{PhaseRampDown, c.RampDownSeconds},
		{PhaseCooldown, c.CooldownSeconds},
	}
}

func (c Config) CycleSeconds() int {
	total := 0
	for _, p := range c.Phases() {
		total += p.Seconds
	}
	return total
}

// At returns the phase, progress within that phase (0..1), and target rate
// at elapsed seconds into the cycle. Ramps use a smooth cosine ease so the
// traffic curve looks organic rather than linear.
func (c Config) At(elapsed float64) (phase string, progress, rate float64) {
	start := 0.0
	for _, p := range c.Phases() {
		end := start + float64(p.Seconds)
		if elapsed < end || p.Name == PhaseCooldown {
			progress = 0
			if p.Seconds > 0 {
				progress = math.Min(1, math.Max(0, (elapsed-start)/float64(p.Seconds)))
			}
			ease := (1 - math.Cos(progress*math.Pi)) / 2
			switch p.Name {
			case PhaseIdle, PhaseCooldown:
				rate = c.BaseRate
			case PhaseRampUp:
				rate = c.BaseRate + (c.PeakRate-c.BaseRate)*ease
			case PhasePeak:
				// A little wobble at the top keeps the chart alive.
				rate = c.PeakRate * (1 + 0.08*math.Sin(progress*4*math.Pi))
			case PhaseRampDown:
				rate = c.PeakRate - (c.PeakRate-c.BaseRate)*ease
			}
			return p.Name, progress, rate
		}
		start = end
	}
	return PhaseCooldown, 1, c.BaseRate
}

// Validate rejects configs that would break the cycle.
func (c Config) Validate() error {
	switch {
	case c.TickSeconds < 1 || c.TickSeconds > 30:
		return errors.New("tickSeconds must be 1..30")
	case c.BaseRate < 0 || c.PeakRate < c.BaseRate || c.PeakRate > 500:
		return errors.New("rates must satisfy 0 <= baseRate <= peakRate <= 500")
	case c.CycleSeconds() < 30:
		return errors.New("cycle must be at least 30s")
	case c.IdleSeconds < 0 || c.RampUpSeconds < 0 || c.PeakSeconds < 0 || c.RampDownSeconds < 0 || c.CooldownSeconds < 0:
		return errors.New("phase durations must be >= 0")
	case c.RolloutEveryNCycles < 0:
		return errors.New("rolloutEveryNCycles must be >= 0")
	}
	return nil
}
