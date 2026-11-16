package app

// RunPendingJobs computes every queued slow-metric job now, standing in
// for the background worker.
func RunPendingJobs(a *Analyzer) {
	for {
		job, ok := a.nextJob()
		if !ok {
			return
		}
		a.runJob(job)
	}
}
