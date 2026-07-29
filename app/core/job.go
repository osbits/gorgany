package core

import (
	"context"
	"errors"
	"time"
)

// ErrJobScheduleMissingInterval is returned for a schedule with no positive
// interval, which cannot be run.
var ErrJobScheduleMissingInterval = errors.New(
	"job schedule: Every must be a positive duration")

// JobSchedule describes when a job should run.
//
// It is a plain value type the framework owns. IJob used to return
// `*gocron.Job`, which leaked a third-party type into a core interface: every
// app's job files had to import github.com/jasonlvhit/gocron directly, so the
// dependency could never be replaced without breaking all of them. Worse, the only
// way to obtain a *gocron.Job was gocron.Every(), which registers on gocron's
// package-level default scheduler — a global the provider held no handle on. That
// is why no scheduled job in any gorgany app has ever run.
type JobSchedule struct {
	// Every is the interval between runs. Required, and must be positive.
	Every time.Duration

	// RunAtStartup runs the job once as soon as the scheduler starts, instead of
	// waiting a full interval for the first run. A cache warmer wants this; a
	// nightly report does not.
	RunAtStartup bool

	// AllowOverlap permits a run to begin while the previous one is still going.
	//
	// The default (false) skips a tick whose predecessor has not finished, which is
	// what you want for anything touching shared state: a job that occasionally
	// runs longer than its interval must not pile up copies of itself.
	AllowOverlap bool
}

// Validate reports whether the schedule can actually be run.
func (s JobSchedule) Validate() error {
	if s.Every <= 0 {
		return ErrJobScheduleMissingInterval
	}
	return nil
}

// IJob is a unit of work run on a schedule.
type IJob interface {
	// Schedule reports when this job should run.
	Schedule() JobSchedule

	// Run performs the work.
	//
	// The context is cancelled when the application shuts down, so a long-running
	// job can stop promptly instead of being killed mid-flight. A returned error is
	// logged; it does not unschedule the job.
	Run(ctx context.Context) error
}
