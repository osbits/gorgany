// Package session holds the session-maintenance commands.
package session

import (
	"context"
	"fmt"

	"github.com/spf13/viper"

	"github.com/osbits/gorgany/v2/app/core"
)

// GcCommand deletes every expired session.
//
// It exists because a scheduled sweep is not always the right shape. JobProvider now adds
// ClearExpiredSessionsJob itself for a database-backed app, but that only helps a process
// that runs the scheduler: an app that deploys its web tier without JobProvider, or runs
// maintenance from an external cron — which is what the framework's interval-only
// core.JobSchedule pushes wall-clock work towards — has nowhere to hang the sweep. This gives
// it one:
//
//	go run . session:gc
//
// The job and this command call the same ISessionStorage.ClearExpiredSessions, so the two
// cannot drift.
type GcCommand struct {
	SessionStorage core.ISessionStorage `container:"inject"`
}

var _ core.ICommand = (*GcCommand)(nil)

func (thiz GcCommand) GetName() string {
	return "session:gc"
}

func (thiz GcCommand) Execute(_ context.Context) {
	if thiz.SessionStorage == nil {
		fmt.Println("session:gc: no session storage is configured")
		return
	}

	// Memory storage lives in the web process's heap, so sweeping it from a separate CLI
	// process empties a map that was created moments ago and is about to be discarded. Say
	// so rather than printing a success that means nothing — this is the single most likely
	// way to wire the command up wrong.
	if storage := viper.GetString("auth.session.storage"); storage != "database" {
		fmt.Printf("session:gc: auth.session.storage is %q, so sessions live in the web "+
			"process's memory and there is nothing for a separate process to sweep; this "+
			"command is for `database` storage\n", storage)
		return
	}

	thiz.SessionStorage.ClearExpiredSessions()
	fmt.Println("session:gc: expired sessions cleared")
}
