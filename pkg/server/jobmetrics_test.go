package server

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestJobOutcomesRecordAndSnapshot(t *testing.T) {
	j := newJobOutcomes()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	j.now = func() time.Time { return at }
	if got := j.snapshotAndReset(); got != nil {
		t.Fatalf("empty snapshot = %v, want nil", got)
	}
	boom := errors.New("boom")
	j.record(JobRegistrySync, nil)
	j.record(JobRegistrySync, boom)
	j.record(JobRegistrySync, boom)
	j.record(JobCatalogRefresh, boom)
	j.record("user-controlled-"+"junk", nil)
	got := j.snapshotAndReset()
	want := []jobSample{
		{JobCatalogRefresh, jobOutcomeError, 1},
		{JobRegistrySync, jobOutcomeError, 2},
		{JobRegistrySync, jobOutcomeOK, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
	if again := j.snapshotAndReset(); again != nil {
		t.Fatalf("snapshot after reset = %v, want nil", again)
	}
	last := j.lastSuccesses()
	if len(last) != 1 || !last[JobRegistrySync].Equal(at) {
		t.Fatalf("lastSuccesses = %v, want only %s at %s", last, JobRegistrySync, at)
	}
}

func TestJobOutcomesLogSnapshot(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	j := newJobOutcomes()
	j.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	j.record(JobCatalogRefresh, nil)
	j.record(JobRegistrySync, nil)
	j.record(JobRegistrySync, errors.New("boom"))
	j.logSnapshot()
	out := buf.String()
	for _, want := range []string{
		`"msg":"metrics job","job":"catalog_refresh","outcome":"ok","count":1`,
		`"msg":"metrics job","job":"registry_sync","outcome":"error","count":1`,
		`"msg":"metrics job last success","job":"catalog_refresh","last_success":"2026-01-02T03:04:05Z"`,
		`"msg":"metrics job last success","job":"registry_sync","last_success":"2026-01-02T03:04:05Z"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %s:\n%s", want, out)
		}
	}
	if again := j.snapshotAndReset(); again != nil {
		t.Fatalf("logSnapshot must reset counts, got %v", again)
	}
	buf.Reset()
	j.logSnapshot()
	if strings.Contains(buf.String(), `"msg":"metrics job"`) || !strings.Contains(buf.String(), "last success") {
		t.Errorf("second flush should only repeat last success:\n%s", buf.String())
	}
}

func TestRecordJobGlobal(t *testing.T) {
	jobStats.snapshotAndReset()
	RecordJob(JobCatalogRefresh, errors.New("x"))
	got := jobStats.snapshotAndReset()
	if len(got) != 1 || got[0].Job != JobCatalogRefresh || got[0].Outcome != jobOutcomeError {
		t.Fatalf("snapshot = %v", got)
	}
}

func TestJobOutcomesTotalsMonotonic(t *testing.T) {
	j := newJobOutcomes()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	j.now = func() time.Time { return at }
	j.record(JobRegistrySync, nil)
	j.snapshotAndReset()
	j.record(JobRegistrySync, nil)
	j.record(JobCatalogRefresh, errors.New("boom"))

	var buf bytes.Buffer
	j.writeProm(&buf)
	out := buf.String()
	for _, want := range []string{
		`dibs_background_job_runs_total{job="registry_sync",result="ok"} 2` + "\n",
		`dibs_background_job_runs_total{job="registry_sync",result="error"} 0` + "\n",
		`dibs_background_job_runs_total{job="catalog_refresh",result="error"} 1` + "\n",
		`dibs_background_job_last_success_timestamp_seconds{job="registry_sync"} ` + "1767323045\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `last_success_timestamp_seconds{job="catalog_refresh"}`) {
		t.Errorf("catalog_refresh never succeeded; must have no timestamp:\n%s", out)
	}
}
