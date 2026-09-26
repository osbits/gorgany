package provider

import (
	grgprovider "github.com/osbits/gorgany/v2/provider"
)

// newJobProvider is the scheduler and its jobs. Server only: its Boot starts the
// scheduler.
func newJobProvider() *grgprovider.JobProvider {
	p := grgprovider.NewJobProvider()
	// p.AddJob(func() core.IJob { return &job.NotesCleanupJob{} })
	return p
}
