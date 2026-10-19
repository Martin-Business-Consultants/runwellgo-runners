package runners

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/runwellgo/internal/plugin"
)

// What reaches people as jobs move: a job's state and new log lines pushed
// to the open tabs of everyone on staff (live), and a notification to the
// person who asked when it needs them or it's over (notify). Both are best
// effort: the routes stay the truth.

// staff is who's on staff, kept for a minute: whom live events go to.
var staff struct {
	mu  sync.Mutex
	ids []int64
	at  time.Time
}

func staffIDs(ctx context.Context, api *plugin.API) []int64 {
	staff.mu.Lock()
	defer staff.mu.Unlock()
	if time.Since(staff.at) < time.Minute && staff.ids != nil {
		return staff.ids
	}
	people, err := api.Records(ctx, plugin.Query{Type: "User", Open: true, Limit: 500})
	if err != nil {
		api.Log("live: " + err.Error())
		return staff.ids
	}
	ids := []int64{}
	for _, p := range people {
		ids = append(ids, intOf(p["id"]))
	}
	staff.ids, staff.at = ids, time.Now()
	return ids
}

// publish pushes an event to everyone on staff with a tab open.
func publish(ctx context.Context, api *plugin.API, topic string, data any) {
	if err := api.Publish(ctx, topic, staffIDs(ctx, api), data); err != nil {
		api.Log("live " + topic + ": " + err.Error())
	}
}

// publishJob tells open tabs a job changed (its page and lists read it
// again, or take it as it is).
func publishJob(ctx context.Context, api *plugin.API, j *Job) {
	publish(ctx, api, "job", map[string]any{"id": j.ID, "record": j.Record, "status": j.Status, "status_label": statusLabel(j.Status),
		"pr_url": nullable(j.PRURL), "active": active(j.Status)})
}

// publishEvents sends a job's new log lines, as many as fit in a live
// event; a tab that misses some reads them from the route.
func publishEvents(ctx context.Context, api *plugin.API, jobID int64, lines []Event) {
	size, cut := 0, len(lines)
	for i, e := range lines {
		size += len(e.Text) + 64
		if size > 48<<10 {
			cut = i
			break
		}
	}
	publish(ctx, api, "events", map[string]any{"job_id": jobID, "events": lines[:cut], "more": cut < len(lines)})
}

func publishRunner(ctx context.Context, api *plugin.API, m *Runner) {
	publish(ctx, api, "runner", map[string]any{"id": m.ID, "name": m.Name})
}

// notifyRequester tells the person who asked how their job went.
func notifyRequester(ctx context.Context, api *plugin.API, j *Job) {
	var title, body string
	work := j.RecordTitle
	if work == "" {
		work = j.Record
	}
	switch j.Status {
	case statusAwaiting:
		title = "A runner finished: approve its changes"
		body = fmt.Sprintf("%s on %s is ready for you to look at.", work, j.Repo)
		if j.PRURL != "" {
			body += " " + j.PRURL
		}
	case statusSucceeded:
		title = "A runner finished with nothing to change"
		body = fmt.Sprintf("%s on %s: %s", work, j.Repo, firstLine(j.Summary, "no summary"))
	case statusFailed:
		title = "A runner job failed"
		body = fmt.Sprintf("%s on %s: %s", work, j.Repo, firstLine(j.Error, "it stopped"))
	default:
		return
	}
	err := api.Notify(ctx, plugin.Notification{UserID: j.RequestedByID, Title: title, Body: body, Path: fmt.Sprintf("/runners/jobs/%d", j.ID),
		Source: "Runners", Subject: work, Group: fmt.Sprintf("runners-job-%d", j.ID)})
	if err != nil {
		api.Log("notify: " + err.Error())
	}
}

func firstLine(s, otherwise string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return otherwise
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

// ---- approvals --------------------------------------------------------------------------

// requestApproval raises the approval for a finished job with changes,
// for the AI plugin's Approvals (runners.approval.requested): what it is,
// where its pull request is, and the tools that decide it.
func requestApproval(ctx context.Context, api *plugin.API, j *Job, runnerName string) {
	emit(ctx, api, "runners.approval.requested", approvalPayload(j, runnerName))
}

func approvalPayload(j *Job, runnerName string) map[string]any {
	var d strings.Builder
	fmt.Fprintf(&d, "**Repository:** %s", j.Repo)
	if j.Branch != "" {
		fmt.Fprintf(&d, " (branch `%s`)", j.Branch)
	}
	d.WriteString("\n\n")
	if runnerName != "" {
		fmt.Fprintf(&d, "**Runner:** %s\n\n", runnerName)
	}
	if j.DurationSeconds > 0 {
		fmt.Fprintf(&d, "**Took:** %s\n\n", duration(j.DurationSeconds))
	}
	if len(j.FilesChanged) > 0 {
		fmt.Fprintf(&d, "**Files changed (%d):**\n", len(j.FilesChanged))
		for i, f := range j.FilesChanged {
			if i == 50 {
				fmt.Fprintf(&d, "- …and %d more\n", len(j.FilesChanged)-50)
				break
			}
			fmt.Fprintf(&d, "- `%s`\n", f)
		}
		d.WriteString("\n")
	}
	if j.Summary != "" {
		fmt.Fprintf(&d, "**The agent's summary:**\n\n%s\n", j.Summary)
	}
	title := "Approve the runner's changes"
	if j.RecordTitle != "" {
		title += ": " + j.RecordTitle
	}
	return map[string]any{
		"id": strconv.FormatInt(j.ID, 10), "kind": "runner_job", "title": title,
		"summary": fmt.Sprintf("A runner changed %d files in %s for %s.", len(j.FilesChanged), j.Repo, orString(j.RecordTitle, j.Record)),
		"record":  j.Record, "link": j.PRURL, "details_markdown": d.String(), "requested_by": j.RequestedByID,
		"approvers": map[string]any{"user_ids": []int64{j.RequestedByID}},
		"approve":   map[string]any{"tool": "approve_runner_job", "args": map[string]any{"job_id": j.ID}},
		"decline":   map[string]any{"tool": "decline_runner_job", "args": map[string]any{"job_id": j.ID}},
	}
}

// withdrawApproval takes an approval back (the job was cancelled or tried
// again before anyone decided).
func withdrawApproval(ctx context.Context, api *plugin.API, j *Job) {
	emit(ctx, api, "runners.approval.withdrawn", map[string]any{"id": strconv.FormatInt(j.ID, 10)})
}

func orString(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

// duration is seconds in words: 45s, 3m 20s, 1h 5m.
func duration(s int64) string {
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
}
