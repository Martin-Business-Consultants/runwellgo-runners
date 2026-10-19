package runners

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Martin-Business-Consultants/runwellgo/internal/plugin"
)

// Jobs, as people (and their agents) see them: hand work to a runner,
// follow it, cancel or retry it, and decide on what it made.

func caller(req *plugin.Request) (*plugin.Caller, *plugin.Response) {
	if req.Actor == nil {
		return nil, refuse(http.StatusUnauthorized, "Sign in first.")
	}
	return req.Actor, nil
}

// mayDecide is whether the caller may cancel, retry or decide on a job:
// the person who asked, or someone who manages runners.
func mayDecide(c *plugin.Caller, j *Job) bool {
	return c.ID == j.RequestedByID || c.Can(capManage)
}

func badge(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	var n int
	if err := api.QueryRow(ctx, `SELECT count(*) FROM runners_jobs WHERE status IN (?,?,?)`, statusQueued, statusClaimed, statusRunning).Scan(&n); err != nil {
		return nil, err
	}
	return ok(map[string]any{"count": n})
}

func listJobs(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	sweep(ctx, api)
	where, args := []string{"1=1"}, []any{}
	status := req.Query.Get("status")
	switch status {
	case "", "all":
		status = "all"
	case "active":
		where = append(where, "status IN (?,?,?)")
		args = append(args, statusQueued, statusClaimed, statusRunning)
	case "done":
		where = append(where, "status IN (?,?)")
		args = append(args, statusApproved, statusSucceeded)
	default:
		if !slices.Contains(allStatuses, status) {
			return invalid("status", "is active, awaiting_approval, done, failed, or one status"), nil
		}
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if r := strings.TrimSpace(req.Query.Get("runner")); r != "" && r != "all" {
		where = append(where, "claimed_by_id = ?")
		args = append(args, intOf(r))
	}
	if p := strings.TrimSpace(req.Query.Get("person")); p != "" && p != "all" {
		id := intOf(p)
		if p == "me" && req.Actor != nil {
			id = req.Actor.ID
		}
		where = append(where, "requested_by_id = ?")
		args = append(args, id)
	}
	if rec := strings.TrimSpace(req.Query.Get("record")); rec != "" {
		where = append(where, "record = ?")
		args = append(args, rec)
	}
	jobs, err := queryJobs(ctx, api, strings.Join(where, " AND ")+" ORDER BY id DESC LIMIT 50", args...)
	if err != nil {
		return nil, err
	}
	names, err := runnerNames(ctx, api)
	if err != nil {
		return nil, err
	}
	list := []map[string]any{}
	for _, j := range jobs {
		list = append(list, jobJSON(j, names))
	}
	// What the filters offer: the runners and the people who asked.
	runners := []map[string]any{{"value": "all", "label": "Any runner"}}
	for _, m := range sortedNames(names) {
		runners = append(runners, map[string]any{"value": strconv.FormatInt(m.id, 10), "label": m.name})
	}
	// Each person by the name on their newest job (a removed person's jobs
	// are their successor's, under the name of whoever asked).
	people := []map[string]any{{"value": "all", "label": "Anyone"}}
	rows, err := api.Query(ctx, `SELECT requested_by_id, COALESCE(requested_by_name, '') FROM runners_jobs j
		WHERE id = (SELECT MAX(id) FROM runners_jobs WHERE requested_by_id = j.requested_by_id) ORDER BY 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		people = append(people, map[string]any{"value": strconv.FormatInt(id, 10), "label": orString(name, fmt.Sprintf("Person %d", id))})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	crows, err := api.Query(ctx, `SELECT status, count(*) FROM runners_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var s string
		var n int
		if err := crows.Scan(&s, &n); err != nil {
			return nil, err
		}
		counts[s] = n
	}
	if err := crows.Err(); err != nil {
		return nil, err
	}
	var anyRunner int
	_ = api.QueryRow(ctx, `SELECT count(*) FROM runners_machines`).Scan(&anyRunner)
	summary := fmt.Sprintf("%d jobs", len(list))
	if len(list) == 1 {
		summary = "1 job"
	}
	return ok(map[string]any{"summary": summary, "status": status, "jobs": list, "runners": runners, "people": people, "counts": counts,
		"has_runners": anyRunner > 0, "can_manage": req.Actor.Can(capManage)})
}

type named struct {
	id   int64
	name string
}

func sortedNames(m map[int64]string) []named {
	out := []named{}
	for id, n := range m {
		out = append(out, named{id, n})
	}
	slices.SortFunc(out, func(a, b named) int { return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)) })
	return out
}

// jobInput is what a new job is made from, wherever it's asked for.
type jobInput struct {
	Record, Repo, BaseBranch, Instructions string
	RunnerID, Timeout                      int64
}

func readJobInput(req *plugin.Request, wrap string) jobInput {
	p := params(req)
	w := wrapped(p, wrap)
	get := func(k string) any {
		if v, ok := w[k]; ok && str(v) != "" {
			return v
		}
		return p[k]
	}
	in := jobInput{Record: strings.TrimSpace(str(get("record"))), Repo: strings.TrimSpace(str(get("repo"))), BaseBranch: strings.TrimSpace(str(get("base_branch"))),
		Instructions: strings.TrimSpace(str(get("instructions"))), RunnerID: intOf(get("runner_id")), Timeout: intOf(get("timeout_minutes"))}
	if in.Record == "" {
		in.Record = strings.TrimSpace(req.Query.Get("record"))
	}
	return in
}

func createJob(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	c, res := caller(req)
	if res != nil {
		return res, nil
	}
	j, res, err := newJob(ctx, api, req, c, readJobInput(req, "job"))
	if res != nil || err != nil {
		return res, err
	}
	names, _ := runnerNames(ctx, api)
	summary, waiting := queuedSummary(ctx, api, j)
	return plugin.JSON(http.StatusCreated, map[string]any{"summary": summary, "message": summary, "waiting_for_runner": waiting, "job": jobJSON(j, names)}), nil
}

// queuedSummary says a job is queued, and whether a runner can take it.
func queuedSummary(ctx context.Context, api *plugin.API, j *Job) (string, bool) {
	runners, _ := allRunners(ctx, api)
	now := time.Now()
	for _, m := range runners {
		if !m.Paused && m.Online(now) && m.repoAllowed(j.Repo) && (j.MachineID == 0 || j.MachineID == m.ID) {
			return fmt.Sprintf("Handed to a runner: job %d on %s. %s will pick it up in a moment.", j.ID, j.Repo, m.Name), false
		}
	}
	return fmt.Sprintf("Queued job %d on %s. No runner that allows %s is online yet: it starts when one is.", j.ID, j.Repo, j.Repo), true
}

// newJob checks what's asked and queues the job.
func newJob(ctx context.Context, api *plugin.API, req *plugin.Request, c *plugin.Caller, in jobInput) (*Job, *plugin.Response, error) {
	errs := map[string][]string{}
	typ, id, err := plugin.ParseRecord(in.Record)
	if err != nil || typ != "Todo" {
		return nil, invalid("record", `must be a piece of work, like "Todo:18"`), nil //nolint:nilerr // the caller's mistake, answered
	}
	work, err := api.Record(ctx, in.Record)
	if errors.Is(err, plugin.ErrNoRecord) {
		return nil, invalid("record", "isn't a piece of work you can see"), nil
	} else if err != nil {
		return nil, nil, err
	}
	title := str(work["title"])
	if in.Repo == "" {
		if r, ok := repoFor(ctx, api, work); ok {
			in.Repo = r.Repo
			if in.BaseBranch == "" {
				in.BaseBranch = r.BaseBranch
			}
		}
	}
	in.Repo = normalRepo(in.Repo)
	switch {
	case in.Repo == "":
		errs["repo"] = []string{"can't be blank: name one, or set a default for this engagement or client in Settings › Runners"}
	case !validRepo(in.Repo):
		errs["repo"] = []string{"must be owner/name, like acme/website"}
	}
	if in.Instructions == "" {
		errs["instructions"] = []string{"can't be blank"}
	} else if len(in.Instructions) > 20000 {
		errs["instructions"] = []string{"is too long (20,000 characters at most)"}
	}
	if in.BaseBranch != "" && strings.ContainsAny(in.BaseBranch, " \t\n~^:?*[\\") {
		errs["base_branch"] = []string{"isn't a branch name"}
	}
	if in.RunnerID != 0 {
		m, err := findRunner(ctx, api, in.RunnerID)
		switch {
		case errors.Is(err, errNotFound):
			errs["runner_id"] = []string{"isn't one of the runners"}
		case err != nil:
			return nil, nil, err
		case len(errs["repo"]) == 0 && len(m.AllowedRepos) > 0 && !matchesAny(m.AllowedRepos, in.Repo):
			errs["runner_id"] = []string{"doesn't allow " + in.Repo + ": add it to the runner's repositories, or choose another"}
		}
	}
	if in.Timeout == 0 {
		in.Timeout = defaultTimeout
	}
	if in.Timeout < 1 || in.Timeout > 360 {
		errs["timeout_minutes"] = []string{"must be between 1 and 360"}
	}
	if len(errs) > 0 {
		return nil, plugin.Invalid(errs), nil
	}
	brief := workBrief(ctx, api, c, id, work)
	via := ""
	if c.Agent {
		via = orString(c.Via, "an agent")
	}
	now := stamp()
	var machine any
	if in.RunnerID != 0 {
		machine = in.RunnerID
	}
	res, err := api.Exec(ctx, `INSERT INTO runners_jobs (record, record_title, repo, base_branch, instructions, brief, status, machine_id, requested_by_id, requested_by_name, via_agent, timeout_minutes, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("Todo:%d", id), title, in.Repo, nullable(in.BaseBranch), in.Instructions, brief, statusQueued, machine,
		c.ID, c.Name, nullable(via), in.Timeout, now, now)
	if err != nil {
		return nil, nil, err
	}
	jobID, _ := res.LastInsertId()
	if _, err := api.Exec(ctx, `UPDATE runners_jobs SET branch = ? WHERE id = ?`, branchFor(jobID, title), jobID); err != nil {
		return nil, nil, err
	}
	j, err := findJob(ctx, nil, api, jobID)
	if err != nil {
		return nil, nil, err
	}
	publishJob(ctx, api, j)
	return j, nil, nil
}

// workBrief is the work's brief as the person who asked would read it
// (brief_work, through tools), or, without tools, its title and
// description.
func workBrief(ctx context.Context, api *plugin.API, c *plugin.Caller, id int64, work map[string]any) string {
	if api.Grants().Tools {
		out, err := api.RunTool(ctx, c, "brief_work", map[string]any{"id": id})
		if err == nil {
			if data, ok := out["data"].(map[string]any); ok && str(data["brief"]) != "" {
				return str(data["brief"])
			}
			if s := str(out["brief"]); s != "" {
				return s
			}
		} else {
			api.Log("brief_work: " + err.Error())
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", str(work["title"]))
	if e, ok := work["engagement"].(map[string]any); ok {
		fmt.Fprintf(&b, "Engagement: %s\n", str(e["display_name"]))
	}
	if cl, ok := work["client"].(map[string]any); ok {
		fmt.Fprintf(&b, "Client: %s\n", str(cl["display_name"]))
	}
	if d := str(work["description"]); d != "" {
		fmt.Fprintf(&b, "\n%s\n", d)
	}
	return b.String()
}

func jobFor(ctx context.Context, api *plugin.API, req *plugin.Request) (*Job, *plugin.Response, error) {
	j, err := findJob(ctx, nil, api, pathID(req, "job_id"))
	if errors.Is(err, errNotFound) {
		return nil, refuse(http.StatusNotFound, "Couldn't find that job."), nil
	}
	return j, nil, err
}

func showJob(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	sweep(ctx, api)
	j, res, err := jobFor(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	names, err := runnerNames(ctx, api)
	if err != nil {
		return nil, err
	}
	events, err := lastEvents(ctx, api, j.ID, 500)
	if err != nil {
		return nil, err
	}
	o := jobJSON(j, names)
	o["brief"] = j.Brief
	c := req.Actor
	o["can_decide"] = mayDecide(c, j)
	o["can_cancel"] = mayDecide(c, j) && (active(j.Status) || j.Status == statusAwaiting)
	o["can_retry"] = mayDecide(c, j) && !active(j.Status) && j.Status != statusApproved
	o["can_approve"] = mayDecide(c, j) && j.Status == statusAwaiting
	if j.Status == statusQueued {
		// Whether any runner that's on and allows the repository can take it.
		msg, stuck := queuedSummary(ctx, api, j)
		o["waiting"] = map[string]any{"for_runner": stuck, "message": msg}
	}
	summary := fmt.Sprintf("Job %d on %s: %s", j.ID, j.Repo, strings.ToLower(statusLabel(j.Status)))
	if j.PRURL != "" {
		summary += " (" + j.PRURL + ")"
	}
	return ok(map[string]any{"summary": summary, "job": o, "events": events, "last_seq": j.LastSeq})
}

func jobEvents(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	j, res, err := jobFor(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	events, err := jobEventsAfter(ctx, api, j.ID, intOf(req.Query.Get("after")), 1000)
	if err != nil {
		return nil, err
	}
	return ok(map[string]any{"events": events, "last_seq": j.LastSeq, "status": j.Status})
}

func cancelJob(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	j, res, err := jobFor(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	if !mayDecide(req.Actor, j) {
		return refuse(http.StatusForbidden, "Only the person who asked, or someone who manages runners, can cancel it."), nil
	}
	if !active(j.Status) && j.Status != statusAwaiting {
		return refuse(http.StatusConflict, "It's "+strings.ToLower(statusLabel(j.Status))+": there's nothing to cancel."), nil
	}
	was := j.Status
	cleanup := ""
	if was == statusAwaiting && (j.PRURL != "" || j.Branch != "") {
		cleanup = "pending" // its branch and pull request go
	}
	now := stamp()
	r, err := api.Exec(ctx, `UPDATE runners_jobs SET status = ?, cancel_requested = 1, cleanup = NULLIF(?, ''), finished_at = COALESCE(finished_at, ?), updated_at = ? WHERE id = ? AND status = ?`,
		statusCancelled, cleanup, now, now, j.ID, was)
	if err != nil {
		return nil, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return refuse(http.StatusConflict, "It changed meanwhile: look again."), nil
	}
	if was == statusAwaiting {
		withdrawApproval(ctx, api, j)
	}
	if was != statusQueued {
		if lines, err := appendLog(ctx, api, j.ID, "runner", "Cancelled by "+req.Actor.Name+"."); err == nil {
			publishEvents(ctx, api, j.ID, lines)
		}
	}
	j, _ = findJob(ctx, nil, api, j.ID)
	publishJob(ctx, api, j)
	msg := fmt.Sprintf("Cancelled job %d.", j.ID)
	if was == statusClaimed || was == statusRunning {
		msg += " The runner stops within a few seconds."
	}
	names, _ := runnerNames(ctx, api)
	return ok(map[string]any{"summary": msg, "message": msg, "job": jobJSON(j, names)})
}

// appendLog adds one line of the plugin's own to a job's log.
func appendLog(ctx context.Context, api *plugin.API, jobID int64, stream, text string) ([]Event, error) {
	var out []Event
	err := api.Tx(ctx, func(tx plugin.Querier) error {
		j, err := findJob(ctx, tx, api, jobID)
		if err != nil {
			return err
		}
		out, err = addEvents(ctx, tx, j, []Event{{Stream: stream, Text: text}})
		return err
	})
	return out, err
}

func retryJob(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	j, res, err := jobFor(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	if !mayDecide(req.Actor, j) {
		return refuse(http.StatusForbidden, "Only the person who asked, or someone who manages runners, can try it again."), nil
	}
	if active(j.Status) || j.Status == statusApproved {
		return refuse(http.StatusConflict, "It's "+strings.ToLower(statusLabel(j.Status))+": there's nothing to try again."), nil
	}
	if j.Status == statusAwaiting {
		// Tried again before anyone decided: this one's pull request goes.
		now := stamp()
		if _, err := api.Exec(ctx, `UPDATE runners_jobs SET status = ?, cleanup = 'pending', updated_at = ? WHERE id = ? AND status = ?`, statusCancelled, now, j.ID, statusAwaiting); err != nil {
			return nil, err
		}
		withdrawApproval(ctx, api, j)
		if old, err := findJob(ctx, nil, api, j.ID); err == nil {
			publishJob(ctx, api, old)
		}
	}
	nj, res, err := newJob(ctx, api, req, req.Actor, jobInput{Record: j.Record, Repo: j.Repo, BaseBranch: j.BaseBranch, Instructions: j.Instructions, RunnerID: j.MachineID, Timeout: j.TimeoutMinutes})
	if res != nil || err != nil {
		return res, err
	}
	if _, err := api.Exec(ctx, `UPDATE runners_jobs SET retry_of_id = ? WHERE id = ?`, j.ID, nj.ID); err != nil {
		return nil, err
	}
	nj.RetryOfID = j.ID
	names, _ := runnerNames(ctx, api)
	msg := fmt.Sprintf("Trying again as job %d.", nj.ID)
	return plugin.JSON(http.StatusCreated, map[string]any{"summary": msg, "message": msg, "job": jobJSON(nj, names)}), nil
}

// decision reads a decision's note.
func decision(req *plugin.Request) string {
	p := params(req)
	return strings.TrimSpace(str(wrapped(p, "decision")["note"]))
}

func approveJob(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	j, res, err := decide(ctx, api, req, statusApproved)
	if res != nil || err != nil {
		return res, err
	}
	// Runwell acts on it now, as the person deciding: a note with the
	// pull request and summary, and the work in review.
	var b strings.Builder
	b.WriteString("<p><strong>A runner's changes were approved")
	if req.Actor.Name != "" {
		b.WriteString(" by " + html.EscapeString(req.Actor.Name))
	}
	b.WriteString(".</strong></p>")
	if j.PRURL != "" {
		fmt.Fprintf(&b, `<p>Pull request: <a href="%s">%s</a></p>`, html.EscapeString(j.PRURL), html.EscapeString(j.PRURL))
	} else if j.Branch != "" {
		fmt.Fprintf(&b, "<p>Branch <code>%s</code> on %s.</p>", html.EscapeString(j.Branch), html.EscapeString(j.Repo))
	}
	if j.Summary != "" {
		fmt.Fprintf(&b, "<p>%s</p>", strings.ReplaceAll(html.EscapeString(j.Summary), "\n", "<br>"))
	}
	if j.DecisionNote != "" {
		fmt.Fprintf(&b, "<p>Note: %s</p>", html.EscapeString(j.DecisionNote))
	}
	if _, err := api.AddNote(ctx, plugin.Note{Record: j.Record, Body: b.String()}); err != nil {
		undoDecision(ctx, api, j)
		return failed(err)
	}
	review := "in_review"
	if _, err := api.ChangeWork(ctx, plugin.WorkChange{ID: j.workID(), Status: &review}); err != nil {
		api.Log("approve: moving the work to in_review: " + err.Error())
	}
	return decided(ctx, api, j, "Approved. The work has a note with the pull request, and it's in review.")
}

func declineJob(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	j, res, err := decide(ctx, api, req, statusDeclined)
	if res != nil || err != nil {
		return res, err
	}
	body := "<p><strong>A runner's changes were declined"
	if req.Actor.Name != "" {
		body += " by " + html.EscapeString(req.Actor.Name)
	}
	body += ".</strong> Its pull request will be closed and its branch deleted.</p>"
	if j.DecisionNote != "" {
		body += "<p>Note: " + html.EscapeString(j.DecisionNote) + "</p>"
	}
	if _, err := api.AddNote(ctx, plugin.Note{Record: j.Record, Body: body}); err != nil {
		api.Log("decline: the note: " + err.Error())
	}
	return decided(ctx, api, j, "Declined. The runner closes the pull request and deletes the branch on its next check.")
}

// decide moves a job awaiting approval to approved or declined, once.
func decide(ctx context.Context, api *plugin.API, req *plugin.Request, to string) (*Job, *plugin.Response, error) {
	j, res, err := jobFor(ctx, api, req)
	if res != nil || err != nil {
		return nil, res, err
	}
	c := req.Actor
	if !mayDecide(c, j) {
		return nil, refuse(http.StatusForbidden, "Only the person who asked, or someone who manages runners, can decide on it."), nil
	}
	if j.Status != statusAwaiting {
		return nil, refuse(http.StatusConflict, "It isn't waiting for approval: it's "+strings.ToLower(statusLabel(j.Status))+"."), nil
	}
	note := decision(req)
	cleanup := ""
	if to == statusDeclined {
		cleanup = "pending"
	}
	now := stamp()
	r, err := api.Exec(ctx, `UPDATE runners_jobs SET status = ?, decision_note = ?, decided_by_id = ?, decided_by_name = ?, decided_at = ?, cleanup = NULLIF(?, ''), updated_at = ? WHERE id = ? AND status = ?`,
		to, nullable(note), c.ID, c.Name, now, cleanup, now, j.ID, statusAwaiting)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return nil, refuse(http.StatusConflict, "Someone decided on it meanwhile."), nil
	}
	j.Status, j.DecisionNote, j.DecidedByID, j.DecidedByName, j.DecidedAt = to, note, c.ID, c.Name, now
	return j, nil, nil
}

// undoDecision puts a job back to waiting when acting on the approval failed.
func undoDecision(ctx context.Context, api *plugin.API, j *Job) {
	_, _ = api.Exec(ctx, `UPDATE runners_jobs SET status = ?, decision_note = NULL, decided_by_id = NULL, decided_by_name = NULL, decided_at = NULL, cleanup = NULL WHERE id = ?`, statusAwaiting, j.ID)
}

func decided(ctx context.Context, api *plugin.API, j *Job, msg string) (*plugin.Response, error) {
	j, err := findJob(ctx, nil, api, j.ID)
	if err != nil {
		return nil, err
	}
	publishJob(ctx, api, j)
	names, _ := runnerNames(ctx, api)
	return ok(map[string]any{"summary": msg, "message": msg, "job": jobJSON(j, names)})
}

// ---- the quick action's form ---------------------------------------------------------------

// handoffForm is the quick action's form for a piece of work: its default
// repository, instructions from its title and description, and the
// runners to choose from.
func handoffForm(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	record := strings.TrimSpace(req.Query.Get("record"))
	values := map[string]any{"repo": "", "instructions": "", "runner_id": ""}
	var work map[string]any
	if typ, _, err := plugin.ParseRecord(record); err == nil && typ == "Todo" {
		if w, err := api.Record(ctx, record); err == nil {
			work = w
			values["instructions"] = defaultInstructions(api, w)
			if r, ok := repoFor(ctx, api, w); ok {
				values["repo"] = r.Repo
			}
		}
	}
	repos, err := knownRepos(ctx, api)
	if err != nil {
		return nil, err
	}
	if r := str(values["repo"]); r != "" && !slices.Contains(repos, r) {
		repos = append([]string{r}, repos...)
	}
	repoOptions := []map[string]any{}
	for _, r := range repos {
		repoOptions = append(repoOptions, map[string]any{"value": r, "label": r})
	}
	runnerOptions := []map[string]any{{"value": "", "label": "Any runner"}}
	runners, err := allRunners(ctx, api)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, m := range runners {
		label := m.Name
		if !m.Online(now) {
			label += " (offline)"
		} else if m.Paused {
			label += " (paused)"
		}
		runnerOptions = append(runnerOptions, map[string]any{"value": strconv.FormatInt(m.ID, 10), "label": label})
	}
	text := "A runner checks out the repository, works on it with your coding agent and comes back with a pull request for you to approve."
	switch {
	case work == nil:
		text = "Open a piece of work to hand it to a runner."
	case len(runners) == 0:
		text = "There's no runner yet: add one in Settings › Runners, then hand this over."
	case len(repos) == 0:
		text = "No repositories yet: set a default for this engagement or client in Settings › Runners."
	}
	return ok(map[string]any{"text": text, "handoff": values, "repos": repoOptions, "runners": runnerOptions})
}

func defaultInstructions(api *plugin.API, work map[string]any) string {
	s := str(work["title"])
	if d := strings.TrimSpace(api.RichText(str(work["description"])).Text); d != "" {
		s += "\n\n" + d
	}
	return s
}

// knownRepos are the repositories people have used: the defaults, and the
// ones runners allow by name.
func knownRepos(ctx context.Context, api *plugin.API) ([]string, error) {
	out := []string{}
	add := func(r string) {
		r = normalRepo(r)
		if r != "" && !strings.HasSuffix(r, "/*") && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	repos, err := allRepos(ctx, api)
	if err != nil {
		return nil, err
	}
	for _, r := range repos {
		add(r.Repo)
	}
	runners, err := allRunners(ctx, api)
	if err != nil {
		return nil, err
	}
	for _, m := range runners {
		for _, r := range m.AllowedRepos {
			add(r)
		}
		for _, r := range strList(m.Capabilities["allowed_repos"]) {
			add(r)
		}
	}
	slices.Sort(out)
	return out, nil
}

func handoff(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	c, res := caller(req)
	if res != nil {
		return res, nil
	}
	j, res, err := newJob(ctx, api, req, c, readJobInput(req, "handoff"))
	if res != nil || err != nil {
		return res, err
	}
	summary, _ := queuedSummary(ctx, api, j)
	return ok(map[string]any{"summary": summary, "message": summary, "job_id": j.ID, "path": fmt.Sprintf("/runners/jobs/%d", j.ID)})
}

// ---- jobs that stopped ------------------------------------------------------------------

// failStuck gives up on jobs a runner took but stopped reporting on: past
// their time limit (and a grace minute), or with their runner silent for
// a few minutes. Every minute, and on reading jobs.
func failStuck(ctx context.Context, api *plugin.API) error {
	return failStuckAt(ctx, api, sweepClock())
}

// sweepClock is the time stuck jobs are judged at (a test's own).
var sweepClock = time.Now

// lastSweep is when sweep last ran (unix nanoseconds).
var lastSweep atomic.Int64

// sweep runs failStuck at most every ten seconds, when someone reads jobs.
func sweep(ctx context.Context, api *plugin.API) {
	now := time.Now().UnixNano()
	last := lastSweep.Load()
	if now-last < int64(10*time.Second) || !lastSweep.CompareAndSwap(last, now) {
		return
	}
	if err := failStuck(ctx, api); err != nil {
		api.Log("stuck jobs: " + err.Error())
	}
}

func failStuckAt(ctx context.Context, api *plugin.API, now time.Time) error {
	jobs, err := queryJobs(ctx, api, `status IN (?,?) ORDER BY id`, statusClaimed, statusRunning)
	if err != nil {
		return err
	}
	runners, err := allRunners(ctx, api)
	if err != nil {
		return err
	}
	seen := map[int64]string{}
	for _, m := range runners {
		seen[m.ID] = m.LastSeenAt
	}
	for _, j := range jobs {
		started := parseStamp(orString(j.ClaimedAt, j.CreatedAt))
		var why string
		switch last, ok := seen[j.ClaimedByID]; {
		case now.Sub(started) > time.Duration(j.TimeoutMinutes)*time.Minute+time.Minute:
			why = fmt.Sprintf("Timed out: it ran past its %d-minute limit without finishing.", j.TimeoutMinutes)
		case !ok:
			why = "Its runner was removed."
		case now.Sub(parseStamp(orString(last, j.ClaimedAt))) > silentFor:
			why = "Its runner stopped answering."
		default:
			continue
		}
		at := stampOf(now)
		r, err := api.Exec(ctx, `UPDATE runners_jobs SET status = ?, error = ?, cancel_requested = 1, finished_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
			statusFailed, why, at, at, j.ID, j.Status)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			continue
		}
		if lines, err := appendLog(ctx, api, j.ID, "runner", why); err == nil {
			publishEvents(ctx, api, j.ID, lines)
		}
		j.Status, j.Error = statusFailed, why
		publishJob(ctx, api, j)
		notifyRequester(ctx, api, j)
	}
	return nil
}
