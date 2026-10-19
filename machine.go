package runners

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/runwellgo/internal/plugin"
)

// The routes runners call (/api/x/runners/machine/…), each with the
// runner's own token, which the core checks against this plugin's tokens
// before the call gets here (req.Machine). A runner beats, claims one job
// at a time, streams its log, asks whether to stop, finishes, and cleans
// up a declined job's branch. It only ever sees the jobs it claimed.

// machineRunner is the runner calling, by its token.
func machineRunner(ctx context.Context, api *plugin.API, req *plugin.Request) (*Runner, *plugin.Response, error) {
	if req.Machine == nil {
		return nil, refuse(http.StatusUnauthorized, "This route is for runners, with their token."), nil
	}
	m, err := runnerByToken(ctx, api, req.Machine.TokenID)
	if errors.Is(err, errNotFound) {
		return nil, refuse(http.StatusUnauthorized, "This runner was removed from Runwell."), nil
	}
	return m, nil, err
}

// claimedJob is a job this runner claimed.
func claimedJob(ctx context.Context, api *plugin.API, req *plugin.Request, m *Runner) (*Job, *plugin.Response, error) {
	j, err := findJob(ctx, nil, api, pathID(req, "job_id"))
	if errors.Is(err, errNotFound) || err == nil && j.ClaimedByID != m.ID {
		return nil, refuse(http.StatusNotFound, "This runner has no such job."), nil
	}
	return j, nil, err
}

// seen records a runner calling in (and tells open tabs when it comes
// online, so "waiting for it to connect…" moves on).
func seen(ctx context.Context, api *plugin.API, m *Runner, status string, caps map[string]any) error {
	was := m.Online(time.Now()) && m.Status != "new"
	now := stamp()
	if caps != nil {
		b, _ := json.Marshal(caps)
		if _, err := api.Exec(ctx, `UPDATE runners_machines SET status = ?, last_seen_at = ?, capabilities = ?, updated_at = ? WHERE id = ?`, status, now, string(b), now, m.ID); err != nil {
			return err
		}
		m.Capabilities = caps
	} else if _, err := api.Exec(ctx, `UPDATE runners_machines SET last_seen_at = ?, updated_at = ? WHERE id = ?`, now, now, m.ID); err != nil {
		return err
	}
	m.LastSeenAt = now
	if !was || caps != nil && status != m.Status {
		m.Status = status
		publishRunner(ctx, api, m)
	}
	return nil
}

func heartbeat(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	m, res, err := machineRunner(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	p := params(req)
	caps := map[string]any{
		"version": str(p["version"]), "os": str(p["os"]), "arch": str(p["arch"]), "hostname": str(p["hostname"]),
		"tools": p["tools"], "agent": str(p["agent"]), "allowed_repos": strList(p["allowed_repos"]), "max_jobs": intOf(p["max_jobs"]),
	}
	if _, ok := caps["tools"].(map[string]any); !ok {
		caps["tools"] = map[string]any{}
	}
	status := "idle"
	if intOf(p["busy"]) > 0 {
		status = "busy"
	}
	if err := seen(ctx, api, m, status, caps); err != nil {
		return nil, err
	}
	// Jobs it should stop, and declined ones whose branch it should remove.
	cancel := []int64{}
	rows, err := api.Query(ctx, `SELECT id FROM runners_jobs WHERE claimed_by_id = ? AND cancel_requested = 1 AND finished_at IS NULL`, m.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		cancel = append(cancel, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cleanup := []map[string]any{}
	jobs, err := queryJobs(ctx, api, `claimed_by_id = ? AND cleanup = 'pending' ORDER BY id`, m.ID)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		cleanup = append(cleanup, map[string]any{"job_id": j.ID, "repo": j.Repo, "branch": nullable(j.Branch), "pr_url": nullable(j.PRURL)})
	}
	return ok(map[string]any{"name": m.Name, "paused": m.Paused, "poll_seconds": 5, "cancel": cancel, "cleanup": cleanup,
		"allowed_repos": m.AllowedRepos})
}

func claim(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	m, res, err := machineRunner(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	if err := seen(ctx, api, m, m.Status, nil); err != nil {
		return nil, err
	}
	if m.Paused {
		return ok(map[string]any{"job": nil, "paused": true})
	}
	// What it allows now, if it says (else what it said on its last beat).
	if repos, ok := params(req)["allowed_repos"]; ok {
		m.Capabilities["allowed_repos"] = strList(repos)
	}
	queued, err := queryJobs(ctx, api, `status = ? AND (machine_id IS NULL OR machine_id = ?) ORDER BY id LIMIT 100`, statusQueued, m.ID)
	if err != nil {
		return nil, err
	}
	for _, j := range queued {
		if !m.repoAllowed(j.Repo) {
			continue
		}
		now := stamp()
		// One job to one runner: only the first to move it from queued
		// gets it.
		r, err := api.Exec(ctx, `UPDATE runners_jobs SET status = ?, claimed_by_id = ?, claimed_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
			statusClaimed, m.ID, now, now, j.ID, statusQueued)
		if err != nil {
			return nil, err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			continue
		}
		j.Status, j.ClaimedByID, j.ClaimedAt = statusClaimed, m.ID, now
		if lines, err := appendLog(ctx, api, j.ID, "runner", m.Name+" picked it up."); err == nil {
			publishEvents(ctx, api, j.ID, lines)
		}
		publishJob(ctx, api, j)
		site, _ := api.Site(ctx)
		workURL := ""
		if id := j.workID(); id != 0 && site.URL != "" {
			workURL = fmt.Sprintf("%s/work/%d", strings.TrimSuffix(site.URL, "/"), id)
		}
		return ok(map[string]any{"job": map[string]any{
			"id": j.ID, "record": j.Record, "title": j.RecordTitle, "repo": j.Repo, "base_branch": nullable(j.BaseBranch), "branch": j.Branch,
			"instructions": j.Instructions, "brief": j.Brief, "timeout_minutes": j.TimeoutMinutes, "work_url": nullable(workURL),
			"job_url": fmt.Sprintf("%s/runners/jobs/%d", strings.TrimSuffix(site.URL, "/"), j.ID), "requested_by": j.RequestedByName,
		}})
	}
	return ok(map[string]any{"job": nil})
}

// secretish are tokens a log line shouldn't carry, should the runner miss
// one: Runwell's, GitHub's, model providers', and key=value secrets.
var secretish = regexp.MustCompile(`(rwm_|rwt_|ghp_|gho_|ghu_|ghs_|github_pat_|sk-ant-|sk-|xox[abp]-)[A-Za-z0-9_\-]{8,}|(?i)((?:api[_-]?key|token|secret|password)\s*[=:]\s*)\S{6,}`)

func redact(s string) string {
	return secretish.ReplaceAllStringFunc(s, func(m string) string {
		if i := strings.IndexAny(m, "=:"); i > 0 && !strings.HasPrefix(m, "rw") && !strings.HasPrefix(m, "gh") && !strings.HasPrefix(m, "sk-") && !strings.HasPrefix(m, "xox") {
			return m[:i+1] + "[redacted]"
		}
		return "[redacted]"
	})
}

func postEvents(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	m, res, err := machineRunner(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	j, res, err := claimedJob(ctx, api, req, m)
	if res != nil || err != nil {
		return res, err
	}
	if err := seen(ctx, api, m, "busy", nil); err != nil {
		return nil, err
	}
	var body struct {
		Events []Event `json:"events"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return refuse(http.StatusBadRequest, "Send {\"events\": [{\"stream\", \"text\", \"at\"}]}."), nil //nolint:nilerr // the caller's mistake, answered
	}
	lines := []Event{}
	for _, e := range body.Events {
		if len(lines) >= 1000 {
			break
		}
		switch e.Stream {
		case "stdout", "stderr", "runner":
		default:
			e.Stream = "stdout"
		}
		e.Text = redact(strings.TrimRight(e.Text, "\r\n"))
		if len(e.Text) > 8000 {
			e.Text = e.Text[:8000] + "…"
		}
		if t, err := time.Parse(time.RFC3339Nano, e.At); err == nil {
			e.At = stampOf(t)
		} else {
			e.At = ""
		}
		lines = append(lines, e)
	}
	stop := j.CancelRequested || !active(j.Status)
	var added []Event
	err = api.Tx(ctx, func(tx plugin.Querier) error {
		cur, err := findJob(ctx, tx, api, j.ID)
		if err != nil {
			return err
		}
		if cur.Status == statusClaimed {
			now := stamp()
			if _, err := tx.Exec(ctx, `UPDATE runners_jobs SET status = ?, started_at = ?, updated_at = ? WHERE id = ? AND status = ?`, statusRunning, now, now, j.ID, statusClaimed); err != nil {
				return err
			}
			j.Status = statusRunning
		}
		if len(lines) == 0 {
			return nil
		}
		added, err = addEvents(ctx, tx, cur, lines)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(added) > 0 {
		publishEvents(ctx, api, j.ID, added)
	}
	if j.Status == statusRunning && j.StartedAt == "" {
		publishJob(ctx, api, j)
	}
	return ok(map[string]any{"cancel": stop, "status": j.Status})
}

func checkCancel(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	m, res, err := machineRunner(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	j, res, err := claimedJob(ctx, api, req, m)
	if res != nil || err != nil {
		return res, err
	}
	if err := seen(ctx, api, m, "busy", nil); err != nil {
		return nil, err
	}
	return ok(map[string]any{"cancel": j.CancelRequested || !active(j.Status), "status": j.Status})
}

func finish(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	m, res, err := machineRunner(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	j, res, err := claimedJob(ctx, api, req, m)
	if res != nil || err != nil {
		return res, err
	}
	p := params(req)
	outcome := str(p["outcome"])
	prURL := strings.TrimSpace(str(p["pr_url"]))
	if prURL != "" {
		if u, err := url.Parse(prURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return invalid("pr_url", "must be an http(s) address"), nil //nolint:nilerr // the caller's mistake, answered
		}
	}
	branch := strings.TrimSpace(str(p["branch"]))
	if branch == "" {
		branch = j.Branch
	}
	summary := strings.TrimSpace(redact(str(p["summary"])))
	if len(summary) > 20000 {
		summary = summary[:20000] + "…"
	}
	files := strList(p["files_changed"])
	if len(files) > 2000 {
		files = files[:2000]
	}
	filesJSON, _ := json.Marshal(files)
	errText := strings.TrimSpace(redact(str(p["error"])))
	dur := intOf(p["duration_seconds"])
	now := stamp()

	if !active(j.Status) {
		// Cancelled or given up on meanwhile: keep what it says it took,
		// and if it pushed anything, have it removed.
		cleanup := ""
		if outcome == "changes" {
			cleanup = "pending"
		}
		if _, err := api.Exec(ctx, `UPDATE runners_jobs SET duration_seconds = ?, pr_url = COALESCE(NULLIF(?, ''), pr_url), branch = ?, cleanup = COALESCE(NULLIF(?, ''), cleanup), finished_at = COALESCE(finished_at, ?), updated_at = ? WHERE id = ?`,
			dur, prURL, branch, cleanup, now, now, j.ID); err != nil {
			return nil, err
		}
		_ = seen(ctx, api, m, "idle", nil)
		return ok(map[string]any{"status": j.Status, "message": "It was " + strings.ToLower(statusLabel(j.Status)) + " already."})
	}
	var status string
	switch outcome {
	case "changes":
		status = statusAwaiting
	case "no_changes":
		status = statusSucceeded
	case "cancelled":
		status = statusCancelled
	case "failed", "refused":
		status = statusFailed
		if errText == "" {
			errText = "The runner stopped without saying why."
		}
	default:
		return invalid("outcome", "is changes, no_changes, failed, refused or cancelled"), nil
	}
	r, err := api.Exec(ctx, `UPDATE runners_jobs SET status = ?, pr_url = ?, branch = ?, summary = ?, files_changed = ?, duration_seconds = ?, error = ?, finished_at = ?,
		started_at = COALESCE(started_at, claimed_at), updated_at = ? WHERE id = ? AND status IN (?,?)`,
		status, nullable(prURL), branch, nullable(summary), string(filesJSON), dur, nullable(errText), now, now, j.ID, statusClaimed, statusRunning)
	if err != nil {
		return nil, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return refuse(http.StatusConflict, "The job changed meanwhile."), nil
	}
	_ = seen(ctx, api, m, "idle", nil)
	j, err = findJob(ctx, nil, api, j.ID)
	if err != nil {
		return nil, err
	}
	line := map[string]string{statusAwaiting: "Finished with changes: waiting for approval.", statusSucceeded: "Finished: nothing to change.",
		statusFailed: "Failed: " + firstLine(errText, ""), statusCancelled: "Stopped."}[status]
	if lines, err := appendLog(ctx, api, j.ID, "runner", line); err == nil {
		publishEvents(ctx, api, j.ID, lines)
	}
	publishJob(ctx, api, j)
	if status == statusAwaiting {
		requestApproval(ctx, api, j, m.Name)
	}
	notifyRequester(ctx, api, j)
	return ok(map[string]any{"status": j.Status, "message": statusLabel(j.Status)})
}

func cleaned(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	m, res, err := machineRunner(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	j, res, err := claimedJob(ctx, api, req, m)
	if res != nil || err != nil {
		return res, err
	}
	p := params(req)
	state, line := "done", "Deleted the branch."
	if j.PRURL != "" {
		line = "Closed the pull request and deleted the branch."
	}
	if e := strings.TrimSpace(redact(str(p["error"]))); e != "" {
		state, line = "failed", "Couldn't clean up: "+firstLine(e, "")
	}
	if _, err := api.Exec(ctx, `UPDATE runners_jobs SET cleanup = ?, updated_at = ? WHERE id = ?`, state, stamp(), j.ID); err != nil {
		return nil, err
	}
	if lines, err := appendLog(ctx, api, j.ID, "runner", line); err == nil {
		publishEvents(ctx, api, j.ID, lines)
	}
	j.Cleanup = state
	publishJob(ctx, api, j)
	return ok(map[string]any{"cleanup": state})
}
