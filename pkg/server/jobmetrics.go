package server

import (
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Fixed label sets for background-job counters: the only values ever used
// as keys, so the counter stays bounded.
const (
	JobCatalogRefresh = "catalog_refresh"
	JobRegistrySync   = "registry_sync"

	jobOutcomeOK    = "ok"
	jobOutcomeError = "error"
)

var jobNames = map[string]bool{JobCatalogRefresh: true, JobRegistrySync: true}

type jobKey struct{ Job, Outcome string }

type jobSample struct {
	Job, Outcome string
	Count        int64
}

// jobOutcomes counts background-job runs by (job, outcome) and remembers
// each job's last success. Like requestMetrics, window counts feed the log
// and are reset; totals are cumulative for /metrics.
type jobOutcomes struct {
	mu          sync.Mutex
	now         func() time.Time
	counts      map[jobKey]int64
	totals      map[jobKey]int64
	lastSuccess map[string]time.Time
}

var jobStats = newJobOutcomes()

func newJobOutcomes() *jobOutcomes {
	return &jobOutcomes{now: time.Now, counts: map[jobKey]int64{}, totals: map[jobKey]int64{}, lastSuccess: map[string]time.Time{}}
}

// RecordJob records one run of a background job; a nil err is a success.
// Unknown job names are ignored so labels stay bounded.
func RecordJob(job string, err error) { jobStats.record(job, err) }

func (j *jobOutcomes) record(job string, err error) {
	if !jobNames[job] {
		return
	}
	outcome := jobOutcomeOK
	if err != nil {
		outcome = jobOutcomeError
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.counts[jobKey{job, outcome}]++
	j.totals[jobKey{job, outcome}]++
	if err == nil {
		j.lastSuccess[job] = j.now()
	}
}

// snapshotAndReset returns the window counts sorted deterministically and
// clears them; cumulative totals are untouched. Last-success times are kept: they are state, not a window count.
func (j *jobOutcomes) snapshotAndReset() []jobSample {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.counts) == 0 {
		return nil
	}
	samples := make([]jobSample, 0, len(j.counts))
	for k, n := range j.counts {
		samples = append(samples, jobSample{Job: k.Job, Outcome: k.Outcome, Count: n})
	}
	j.counts = map[jobKey]int64{}
	sort.Slice(samples, func(a, b int) bool {
		if samples[a].Job != samples[b].Job {
			return samples[a].Job < samples[b].Job
		}
		return samples[a].Outcome < samples[b].Outcome
	})
	return samples
}

// lastSuccesses returns each job's last success time, keyed by job.
func (j *jobOutcomes) lastSuccesses() map[string]time.Time {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make(map[string]time.Time, len(j.lastSuccess))
	for k, v := range j.lastSuccess {
		out[k] = v
	}
	return out
}

// writeProm writes cumulative run totals and last-success timestamps in
// Prometheus text exposition format. Every known job is emitted with both
// results (zero when unseen) so rate/increase queries see a stable series.
func (j *jobOutcomes) writeProm(w io.Writer) {
	jobs := make([]string, 0, len(jobNames))
	for job := range jobNames {
		jobs = append(jobs, job)
	}
	sort.Strings(jobs)
	j.mu.Lock()
	defer j.mu.Unlock()
	fmt.Fprintln(w, "# HELP dibs_background_job_runs_total Background job runs by job and result.")
	fmt.Fprintln(w, "# TYPE dibs_background_job_runs_total counter")
	for _, job := range jobs {
		for _, outcome := range []string{jobOutcomeError, jobOutcomeOK} {
			fmt.Fprintf(w, "dibs_background_job_runs_total{job=%q,result=%q} %d\n", job, outcome, j.totals[jobKey{job, outcome}])
		}
	}
	fmt.Fprintln(w, "# HELP dibs_background_job_last_success_timestamp_seconds Unix time of each job's last successful run.")
	fmt.Fprintln(w, "# TYPE dibs_background_job_last_success_timestamp_seconds gauge")
	for _, job := range jobs {
		if t, ok := j.lastSuccess[job]; ok {
			fmt.Fprintf(w, "dibs_background_job_last_success_timestamp_seconds{job=%q} %d\n", job, t.Unix())
		}
	}
}

// logSnapshot flushes window counts, then each job's last success, to the log.
func (j *jobOutcomes) logSnapshot() {
	for _, s := range j.snapshotAndReset() {
		slog.Info("metrics job", "job", s.Job, "outcome", s.Outcome, "count", s.Count)
	}
	last := j.lastSuccesses()
	jobs := make([]string, 0, len(last))
	for job := range last {
		jobs = append(jobs, job)
	}
	sort.Strings(jobs)
	for _, job := range jobs {
		slog.Info("metrics job last success", "job", job, "last_success", last[job].UTC().Format(time.RFC3339))
	}
}

func (j *jobOutcomes) logPeriodically(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		j.logSnapshot()
	}
}
