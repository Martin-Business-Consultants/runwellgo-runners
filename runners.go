// Package runners hands code and other heavy work from Runwell to machines
// the install's people control: a piece of work, or an agent acting for
// someone, asks for a job; a runner (cmd/runwell-runner, on a server or a
// laptop with git, gh, ripgrep, mise and a coding agent's CLI) pulls it,
// does it in a clean checkout, streams its log back, and finishes with a
// branch, a pull request and a summary. Runwell itself never runs a shell
// command.
//
// A finished job with changes waits for a person: it raises an approval
// (runners.approval.requested, for the AI plugin's Approvals; the job page
// shows Approve and Decline too), and only an approval makes Runwell act on
// it: a note on the work with the pull request and summary, and the work
// moved to in_review. A decline tells the runner to close the pull request
// and delete the branch.
//
// It reaches Runwell only through the plugin API: its own tables
// (runners_*); "machines" for each runner's token and the routes runners
// call (/machine/…, authenticated by the core against this plugin's
// tokens alone); "live" to push a job's log and state to open tabs;
// "notify" to tell the person who asked how it went; records:read and
// records:write for the work's details, its note and its status; "tools"
// to read the work's brief as the person who asked. With it off, Runwell is
// as it was, and every runner's token stops working.
package runners

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/runwellgo/internal/plugin"
)

// Key is the plugin's key.
const Key = "runners"

const (
	capManage = "runners:manage" // add, pause and remove runners; set default repositories
	capUse    = "runners:use"    // hand work to runners, follow and decide on jobs
)

// Module is the plugin, for the core to register.
func Module() *plugin.Module {
	return &plugin.Module{
		Manifest: plugin.Manifest{
			Key:      Key,
			Name:     "Runners",
			Version:  "1.0.0",
			Author:   "Runwell",
			Homepage: "https://github.com/Martin-Business-Consultants/runwellgo/blob/main/docs/runners.md",
			Description: "Hand code and other heavy work to a machine you control. A runner picks the job up, works in a clean checkout " +
				"with your coding agent, streams its progress here and finishes with a pull request and a summary for you to approve.",
			Permissions: plugin.Grants{RecordsRead: true, RecordsWrite: true, Tables: true, Live: true, Notify: true, Tools: true, Machines: true},
			Schema:      schema,
			Capabilities: []plugin.Capability{
				{Key: capManage, Label: "Add and remove runners", Roles: []string{"owner", "manager"}},
				{Key: capUse, Label: "Hand work to runners", Roles: []string{"owner", "manager", "member"}},
			},
			Routes: routes(),
			Tasks:  []plugin.TaskSpec{{Name: "stuck", Every: "1m"}},
			// A removed person's jobs and runners go to their successor.
			Hooks: []string{"user.removed"},
			Screens: []plugin.Screen{{
				ID: "handoff", Title: "Hand this to a runner", Source: "/handoff", Text: "text",
				Form: &plugin.Form{Record: "handoff", Save: "/handoff", Submit: "Hand it over", Fields: []plugin.Field{
					{Name: "repo", Label: "Repository", Type: "select", OptionsFrom: "repos", Required: true, Help: "owner/name, as gh knows it."},
					{Name: "instructions", Label: "Instructions", Type: "textarea", Required: true},
					{Name: "runner_id", Label: "Runner", Type: "select", OptionsFrom: "runners"},
				}},
			}},
			Nav:          &plugin.NavPage{Label: "Runners", Path: "/runners", Icon: "code", Badge: "/badge"},
			QuickActions: []plugin.QuickAction{{Label: "Hand to a runner", Icon: "code", Types: []string{"Todo"}, Screen: "handoff"}},
		},
		Handlers: map[string]plugin.Handler{
			// people
			"badge": badge, "listRunners": listRunners, "createRunner": createRunner, "showRunner": showRunner,
			"updateRunner": updateRunner, "deleteRunner": deleteRunner,
			"listRepos": listRepos, "saveRepo": saveRepo, "deleteRepo": deleteRepo,
			"listJobs": listJobs, "createJob": createJob, "showJob": showJob, "jobEvents": jobEvents,
			"cancelJob": cancelJob, "retryJob": retryJob, "approveJob": approveJob, "declineJob": declineJob,
			"handoffForm": handoffForm, "handoff": handoff,
			// machines
			"heartbeat": heartbeat, "claim": claim, "postEvents": postEvents, "finish": finish, "checkCancel": checkCancel, "cleaned": cleaned,
		},
		Tasks:   map[string]plugin.Task{"stuck": failStuck},
		OnEvent: onEvent,
	}
}

// onEvent hands a removed person's jobs and runners to their successor:
// the jobs they asked for are the successor's to follow and decide on (and
// their notifications), while requested_by_name keeps who asked; a decision
// they made keeps their name (decided_by_name) and loses the id; runners
// they added are the successor's.
func onEvent(ctx context.Context, api *plugin.API, kind string, data json.RawMessage) error {
	if kind != "user.removed" {
		return nil
	}
	var ev struct {
		Payload struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			SuccessorID int64  `json:"successor_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return err
	}
	p := ev.Payload
	if p.ID == 0 || p.SuccessorID == 0 || p.ID == p.SuccessorID {
		return nil
	}
	now := stamp()
	return api.Tx(ctx, func(q plugin.Querier) error {
		for _, st := range []struct {
			sql  string
			args []any
		}{
			{`UPDATE runners_jobs SET requested_by_name = ? WHERE requested_by_id = ? AND COALESCE(requested_by_name, '') = ''`, []any{p.Name, p.ID}},
			{`UPDATE runners_jobs SET requested_by_id = ?, updated_at = ? WHERE requested_by_id = ?`, []any{p.SuccessorID, now, p.ID}},
			{`UPDATE runners_jobs SET decided_by_name = ? WHERE decided_by_id = ? AND COALESCE(decided_by_name, '') = ''`, []any{p.Name, p.ID}},
			{`UPDATE runners_jobs SET decided_by_id = NULL WHERE decided_by_id = ?`, []any{p.ID}},
			{`UPDATE runners_machines SET created_by_id = ?, updated_at = ? WHERE created_by_id = ?`, []any{p.SuccessorID, now, p.ID}},
		} {
			if _, err := q.Exec(ctx, st.sql, st.args...); err != nil {
				return err
			}
		}
		return nil
	})
}

func routes() []plugin.Route {
	return []plugin.Route{
		{Method: "GET", Path: "/badge", Export: "badge", Capability: capUse},

		{Method: "GET", Path: "/runners", Export: "listRunners", Capability: capUse, Tool: &plugin.Tool{
			Name: "list_runners", Title: "List runners",
			Description: "The machines work can be handed to: whether each is online, paused, its tools and the repositories it allows, and the default repository for clients and engagements.\n\nOften next: start_runner_job.",
		}},
		{Method: "POST", Path: "/runners", Export: "createRunner", Capability: capManage},
		{Method: "GET", Path: "/runners/{id}", Export: "showRunner", Capability: capUse},
		{Method: "PATCH", Path: "/runners/{id}", Export: "updateRunner", Capability: capManage},
		{Method: "DELETE", Path: "/runners/{id}", Export: "deleteRunner", Capability: capManage},

		{Method: "GET", Path: "/repos", Export: "listRepos", Capability: capUse},
		{Method: "PUT", Path: "/repos", Export: "saveRepo", Capability: capManage},
		{Method: "DELETE", Path: "/repos/{id}", Export: "deleteRepo", Capability: capManage},

		{Method: "GET", Path: "/jobs", Export: "listJobs", Capability: capUse, Tool: &plugin.Tool{
			Name: "list_runner_jobs", Title: "List runner jobs",
			Description: "Jobs handed to runners, newest first (50 at most).",
			Query: []plugin.ToolParam{
				{Name: "status", Description: "active (queued, claimed or running), awaiting_approval, done, failed, or one status."},
				{Name: "record", Description: `Only the jobs for this work, "Todo:18".`},
			},
		}},
		{Method: "POST", Path: "/jobs", Export: "createJob", Capability: capUse, Tool: &plugin.Tool{
			Name: "start_runner_job", Title: "Hand work to a runner",
			Description: "Queues a job for a runner: it checks out the repository, runs its coding agent with the instructions and the work's brief, and comes back with a branch, a pull request and a summary for the person to approve. repo defaults to the engagement's or client's.\n\nOften next: show_runner_job.",
			Params: []plugin.ToolParam{
				{Name: "record", Description: `The work it's for, "Todo:18".`, Required: true},
				{Name: "repo", Description: "owner/name (blank: the default for the work's engagement or client)."},
				{Name: "instructions", Description: "What to do, in plain words.", Required: true},
				{Name: "base_branch", Description: "The branch to start from (the repository's default unless set)."},
				{Name: "runner_id", Type: "integer", Description: "One runner from list_runners (blank: any that allows the repository)."},
			},
		}},
		{Method: "GET", Path: "/jobs/{job_id}", Export: "showJob", Capability: capUse, Tool: &plugin.Tool{
			Name: "show_runner_job", Title: "Show a runner job",
			Description: "Its status, runner, pull request, summary, files changed, and the last lines of its log.",
		}},
		{Method: "GET", Path: "/jobs/{job_id}/events", Export: "jobEvents", Capability: capUse},
		{Method: "POST", Path: "/jobs/{job_id}/cancel", Export: "cancelJob", Capability: capUse, Tool: &plugin.Tool{
			Name: "cancel_runner_job", Title: "Cancel a runner job",
			Description: "Stops a job that's queued or running: the runner stops at its next check, within a few seconds.",
		}},
		{Method: "POST", Path: "/jobs/{job_id}/retry", Export: "retryJob", Capability: capUse},
		{Method: "POST", Path: "/jobs/{job_id}/approve", Export: "approveJob", Capability: capUse, Tool: &plugin.Tool{
			Name: "approve_runner_job", Title: "Approve a runner job",
			Description: "Accepts a finished job's pull request: adds a note to the work with the link and summary, and moves the work to in_review.",
			Params:      []plugin.ToolParam{{Name: "note", Description: "Why, or what to do next (optional)."}},
		}},
		{Method: "POST", Path: "/jobs/{job_id}/decline", Export: "declineJob", Capability: capUse, Tool: &plugin.Tool{
			Name: "decline_runner_job", Title: "Decline a runner job",
			Description: "Turns a finished job down: a note on the work says so, and the runner closes the pull request and deletes the branch.",
			Params:      []plugin.ToolParam{{Name: "note", Description: "Why (optional)."}},
		}},
		{Method: "GET", Path: "/handoff", Export: "handoffForm", Capability: capUse},
		{Method: "POST", Path: "/handoff", Export: "handoff", Capability: capUse},

		{Method: "POST", Path: "/machine/heartbeat", Export: "heartbeat", Machine: true},
		{Method: "POST", Path: "/machine/claim", Export: "claim", Machine: true},
		{Method: "POST", Path: "/machine/jobs/{job_id}/events", Export: "postEvents", Machine: true, MaxBody: 2 << 20},
		{Method: "GET", Path: "/machine/jobs/{job_id}/cancel", Export: "checkCancel", Machine: true},
		{Method: "POST", Path: "/machine/jobs/{job_id}/finish", Export: "finish", Machine: true},
		{Method: "POST", Path: "/machine/jobs/{job_id}/cleaned", Export: "cleaned", Machine: true},
	}
}

// ---- answers ---------------------------------------------------------------------------

func refuse(status int, message string) *plugin.Response {
	code := map[int]string{http.StatusNotFound: "not_found", http.StatusForbidden: "forbidden", http.StatusConflict: "conflict",
		http.StatusUnauthorized: "unauthorized"}[status]
	if code == "" {
		code = "invalid"
	}
	return plugin.JSON(status, map[string]any{"error": code, "message": message})
}

func invalid(field, message string) *plugin.Response {
	return plugin.Invalid(map[string][]string{field: {message}})
}

func failed(err error) (*plugin.Response, error) {
	if res := plugin.Answer(err); res != nil {
		return res, nil
	}
	return nil, err
}

func ok(v map[string]any) (*plugin.Response, error) { return plugin.JSON(http.StatusOK, v), nil }

// ---- requests --------------------------------------------------------------------------

func pathID(req *plugin.Request, name string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(req.Params[name]), 10, 64)
	return n
}

func params(req *plugin.Request) map[string]any {
	m := map[string]any{}
	if len(bytes.TrimSpace(req.Body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(req.Body))
		dec.UseNumber()
		_ = dec.Decode(&m)
	}
	return m
}

func wrapped(m map[string]any, key string) map[string]any {
	if w, ok := m[key].(map[string]any); ok {
		return w
	}
	return m
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return fmt.Sprint(v)
}

func intOf(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			f, _ := n.Float64()
			return int64(f)
		}
		return i
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i
	}
	return 0
}

func strList(v any) []string {
	out := []string{}
	switch l := v.(type) {
	case []any:
		for _, x := range l {
			if s := strings.TrimSpace(str(x)); s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range l {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.FieldsFunc(l, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' }) {
			out = append(out, s)
		}
	}
	return out
}

// stamp is now, as the runners tables keep times (UTC, sortable).
func stamp() string { return stampOf(time.Now()) }

func stampOf(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func parseStamp(s string) time.Time {
	t, _ := time.Parse("2006-01-02T15:04:05.000Z", s)
	return t
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func anys[T any](list []T) []any {
	out := make([]any, len(list))
	for i, v := range list {
		out[i] = v
	}
	return out
}

// emit raises one of the plugin's own events for other plugins (the AI
// plugin's Approvals hears runners.approval.*) through the host's generic
// plugin events, where this build has them; without them it does nothing,
// and the job page's own Approve and Decline stand in.
func emit(ctx context.Context, api *plugin.API, name string, data any) {
	if e, ok := any(api).(interface {
		Emit(context.Context, string, any) error
	}); ok {
		if err := e.Emit(ctx, name, data); err != nil {
			api.Log("emit " + name + ": " + err.Error())
		}
	}
}
