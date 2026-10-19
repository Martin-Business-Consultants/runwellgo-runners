package runners

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/runwellgo/internal/plugin"
)

// A job's statuses. queued → claimed (a runner took it) → running (its
// first log lines came) → awaiting_approval (it made changes) → approved or
// declined; or succeeded (it finished with nothing to change), failed, or
// cancelled.
const (
	statusQueued    = "queued"
	statusClaimed   = "claimed"
	statusRunning   = "running"
	statusAwaiting  = "awaiting_approval"
	statusApproved  = "approved"
	statusDeclined  = "declined"
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
)

var allStatuses = []string{statusQueued, statusClaimed, statusRunning, statusAwaiting, statusApproved, statusDeclined, statusSucceeded, statusFailed, statusCancelled}

// active is whether a job is still waiting or being worked on.
func active(status string) bool {
	return status == statusQueued || status == statusClaimed || status == statusRunning
}

// statusLabel is the status in words.
func statusLabel(s string) string {
	return map[string]string{statusQueued: "Queued", statusClaimed: "Starting", statusRunning: "Running", statusAwaiting: "Awaiting approval",
		statusApproved: "Approved", statusDeclined: "Declined", statusSucceeded: "Done, no changes", statusFailed: "Failed", statusCancelled: "Cancelled"}[s]
}

const (
	// onlineFor is how long after its last heartbeat a runner counts as
	// online; it beats every 15 seconds or so.
	onlineFor = 90 * time.Second
	// silentFor is how long a runner with a job may go without a
	// heartbeat before the job is given up on.
	silentFor = 3 * time.Minute
	// maxEvents is the most log lines kept for a job: the oldest go.
	maxEvents = 5000
	// defaultTimeout is a job's limit in minutes unless it says.
	defaultTimeout = 60
)

var errNotFound = errors.New("not found")

// ---- runners ---------------------------------------------------------------------------

// Runner is a machine work can be handed to.
type Runner struct {
	ID           int64
	Name         string
	TokenID      int64
	Status       string // new until it first calls; then idle or busy, as it last said
	LastSeenAt   string
	Capabilities map[string]any
	AllowedRepos []string // Runwell's own limit; empty: whatever the runner allows
	Paused       bool
	CreatedByID  int64
	CreatedAt    string
}

const runnerColumns = `id, name, token_id, status, COALESCE(last_seen_at, ''), COALESCE(capabilities, ''), COALESCE(allowed_repos, ''), paused, COALESCE(created_by_id, 0), created_at`

func scanRunner(r interface{ Scan(...any) error }) (*Runner, error) {
	var m Runner
	var caps, repos string
	if err := r.Scan(&m.ID, &m.Name, &m.TokenID, &m.Status, &m.LastSeenAt, &caps, &repos, &m.Paused, &m.CreatedByID, &m.CreatedAt); err != nil {
		return nil, err
	}
	m.Capabilities = map[string]any{}
	if caps != "" {
		_ = json.Unmarshal([]byte(caps), &m.Capabilities)
	}
	m.AllowedRepos = []string{}
	if repos != "" {
		_ = json.Unmarshal([]byte(repos), &m.AllowedRepos)
	}
	return &m, nil
}

func findRunner(ctx context.Context, api *plugin.API, id int64) (*Runner, error) {
	m, err := scanRunner(api.QueryRow(ctx, `SELECT `+runnerColumns+` FROM runners_machines WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return m, err
}

func runnerByToken(ctx context.Context, api *plugin.API, tokenID int64) (*Runner, error) {
	m, err := scanRunner(api.QueryRow(ctx, `SELECT `+runnerColumns+` FROM runners_machines WHERE token_id = ?`, tokenID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return m, err
}

func allRunners(ctx context.Context, api *plugin.API) ([]*Runner, error) {
	rows, err := api.Query(ctx, `SELECT `+runnerColumns+` FROM runners_machines ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Runner
	for rows.Next() {
		m, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Online is whether it called in lately.
func (m *Runner) Online(now time.Time) bool {
	return m.LastSeenAt != "" && now.Sub(parseStamp(m.LastSeenAt)) < onlineFor
}

// repoAllowed is whether both sides allow repo: Runwell's list for it (when
// it has one) and the runner's own, as it last reported.
func (m *Runner) repoAllowed(repo string) bool {
	repo = normalRepo(repo)
	if len(m.AllowedRepos) > 0 && !matchesAny(m.AllowedRepos, repo) {
		return false
	}
	return matchesAny(strList(m.Capabilities["allowed_repos"]), repo)
}

// matchesAny is whether repo is on the list: an exact owner/name, or
// owner/* for every repository of an owner.
func matchesAny(list []string, repo string) bool {
	repo = normalRepo(repo)
	for _, p := range list {
		p = normalRepo(p)
		if p == repo || (strings.HasSuffix(p, "/*") && strings.HasPrefix(repo, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

// normalRepo is owner/name in lowercase, from a GitHub address too.
func normalRepo(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "file://") {
		return strings.TrimSuffix(s, "/") // a local path keeps its case
	}
	s = strings.ToLower(s)
	for _, p := range []string{"https://github.com/", "http://github.com/", "git@github.com:", "github.com/"} {
		s = strings.TrimPrefix(s, p)
	}
	return strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
}

// validRepo is whether s reads as owner/name (or a local path, which a
// runner allows only when its owner listed it: a test, a mirror).
func validRepo(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "file://") {
		return !strings.ContainsAny(s, " \t\n")
	}
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.IndexFunc(p, func(r rune) bool {
			return !(r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
		}) >= 0 {
			return false
		}
	}
	return true
}

// validPattern is whether s is a repository, or owner/* for an owner's.
func validPattern(s string) bool {
	if strings.HasSuffix(s, "/*") {
		return validRepo(strings.TrimSuffix(s, "*") + "x")
	}
	return validRepo(s)
}

func runnerJSON(m *Runner, now time.Time, active int) map[string]any {
	online := m.Online(now)
	state := "offline"
	switch {
	case m.Status == "new" && m.LastSeenAt == "":
		state = "waiting"
	case m.Paused:
		state = "paused"
	case online && active > 0:
		state = "busy"
	case online:
		state = "online"
	}
	return map[string]any{"id": m.ID, "name": m.Name, "state": state, "online": online, "paused": m.Paused,
		"last_seen_at": nullable(m.LastSeenAt), "capabilities": m.Capabilities, "allowed_repos": m.AllowedRepos,
		"runner_repos": strList(m.Capabilities["allowed_repos"]), "active_jobs": active, "created_at": m.CreatedAt}
}

// ---- jobs ------------------------------------------------------------------------------

// Job is work handed to a runner.
type Job struct {
	ID              int64
	Record          string
	RecordTitle     string
	Repo            string
	BaseBranch      string
	Branch          string
	Instructions    string
	Brief           string
	Status          string
	MachineID       int64 // the runner asked for; 0 for any
	ClaimedByID     int64
	RequestedByID   int64
	RequestedByName string
	ViaAgent        string
	TimeoutMinutes  int64
	CancelRequested bool
	PRURL           string
	Summary         string
	FilesChanged    []string
	DurationSeconds int64
	Error           string
	DecisionNote    string
	DecidedByID     int64
	DecidedByName   string
	DecidedAt       string
	Cleanup         string // pending once declined with a branch to remove; done after
	RetryOfID       int64
	LastSeq         int64
	ClaimedAt       string
	StartedAt       string
	FinishedAt      string
	CreatedAt       string
	UpdatedAt       string
}

const jobColumns = `id, record, COALESCE(record_title, ''), repo, COALESCE(base_branch, ''), COALESCE(branch, ''), instructions, COALESCE(brief, ''), status,
	COALESCE(machine_id, 0), COALESCE(claimed_by_id, 0), requested_by_id, COALESCE(requested_by_name, ''), COALESCE(via_agent, ''), timeout_minutes, cancel_requested,
	COALESCE(pr_url, ''), COALESCE(summary, ''), COALESCE(files_changed, ''), COALESCE(duration_seconds, 0), COALESCE(error, ''), COALESCE(decision_note, ''),
	COALESCE(decided_by_id, 0), COALESCE(decided_by_name, ''), COALESCE(decided_at, ''), COALESCE(cleanup, ''), COALESCE(retry_of_id, 0), last_seq,
	COALESCE(claimed_at, ''), COALESCE(started_at, ''), COALESCE(finished_at, ''), created_at, updated_at`

func scanJob(r interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var files string
	err := r.Scan(&j.ID, &j.Record, &j.RecordTitle, &j.Repo, &j.BaseBranch, &j.Branch, &j.Instructions, &j.Brief, &j.Status,
		&j.MachineID, &j.ClaimedByID, &j.RequestedByID, &j.RequestedByName, &j.ViaAgent, &j.TimeoutMinutes, &j.CancelRequested,
		&j.PRURL, &j.Summary, &files, &j.DurationSeconds, &j.Error, &j.DecisionNote,
		&j.DecidedByID, &j.DecidedByName, &j.DecidedAt, &j.Cleanup, &j.RetryOfID, &j.LastSeq,
		&j.ClaimedAt, &j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return nil, err
	}
	j.FilesChanged = []string{}
	if files != "" {
		_ = json.Unmarshal([]byte(files), &j.FilesChanged)
	}
	return &j, nil
}

func findJob(ctx context.Context, q plugin.Querier, api *plugin.API, id int64) (*Job, error) {
	var row *plugin.Row
	if q != nil {
		row = q.QueryRow(ctx, `SELECT `+jobColumns+` FROM runners_jobs WHERE id = ?`, id)
	} else {
		row = api.QueryRow(ctx, `SELECT `+jobColumns+` FROM runners_jobs WHERE id = ?`, id)
	}
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return j, err
}

func queryJobs(ctx context.Context, api *plugin.API, where string, args ...any) ([]*Job, error) {
	rows, err := api.Query(ctx, `SELECT `+jobColumns+` FROM runners_jobs WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// workID is the id of the work a job is for ("Todo:18" → 18).
func (j *Job) workID() int64 {
	_, id, _ := plugin.ParseRecord(j.Record)
	return id
}

// slug is a few words of the work's title for the branch's name.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// branchFor is the branch a job's work goes on: runwell/<job id>-<slug>.
func branchFor(id int64, title string) string {
	if s := slug(title); s != "" {
		return fmt.Sprintf("runwell/%d-%s", id, s)
	}
	return fmt.Sprintf("runwell/%d", id)
}

// jobJSON is a job as the app and agents see it; names are the runners'.
func jobJSON(j *Job, names map[int64]string) map[string]any {
	var runner any
	if j.ClaimedByID != 0 {
		runner = map[string]any{"id": j.ClaimedByID, "name": names[j.ClaimedByID]}
	}
	var asked any
	if j.MachineID != 0 {
		asked = map[string]any{"id": j.MachineID, "name": names[j.MachineID]}
	}
	path := ""
	if id := j.workID(); id != 0 && strings.HasPrefix(j.Record, "Todo:") {
		path = fmt.Sprintf("/work/%d", id)
	}
	return map[string]any{"id": j.ID, "record": j.Record, "record_title": j.RecordTitle, "record_path": nullable(path),
		"repo": j.Repo, "base_branch": nullable(j.BaseBranch), "branch": nullable(j.Branch), "instructions": j.Instructions,
		"status": j.Status, "status_label": statusLabel(j.Status), "active": active(j.Status),
		"runner": runner, "asked_runner": asked, "requested_by": map[string]any{"id": j.RequestedByID, "name": j.RequestedByName},
		"via_agent": nullable(j.ViaAgent), "timeout_minutes": j.TimeoutMinutes, "cancel_requested": j.CancelRequested,
		"pr_url": nullable(j.PRURL), "summary": nullable(j.Summary), "files_changed": j.FilesChanged, "duration_seconds": j.DurationSeconds,
		"error": nullable(j.Error), "decision_note": nullable(j.DecisionNote), "decided_by": decidedBy(j), "decided_at": nullable(j.DecidedAt),
		"cleanup": nullable(j.Cleanup), "retry_of_id": nullIfZero(j.RetryOfID),
		"claimed_at": nullable(j.ClaimedAt), "started_at": nullable(j.StartedAt), "finished_at": nullable(j.FinishedAt),
		"created_at": j.CreatedAt, "updated_at": j.UpdatedAt, "path": fmt.Sprintf("/runners/jobs/%d", j.ID)}
}

func decidedBy(j *Job) any {
	switch {
	case j.DecidedByID != 0:
		return map[string]any{"id": j.DecidedByID, "name": j.DecidedByName}
	case j.DecidedByName != "":
		// Someone since removed: their name, no longer a person to link.
		return map[string]any{"id": nil, "name": j.DecidedByName}
	}
	return nil
}

func nullIfZero(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// runnerNames are every runner's name, by id, for the jobs a list shows.
func runnerNames(ctx context.Context, api *plugin.API) (map[int64]string, error) {
	rows, err := api.Query(ctx, `SELECT id, name FROM runners_machines`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// ---- events ----------------------------------------------------------------------------

// Event is a line of a job's log.
type Event struct {
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"` // stdout, stderr or runner (the runner's own steps)
	Text   string `json:"text"`
	At     string `json:"at"`
}

func jobEventsAfter(ctx context.Context, api *plugin.API, jobID, after int64, limit int) ([]Event, error) {
	rows, err := api.Query(ctx, `SELECT seq, stream, text, at FROM runners_events WHERE job_id = ? AND seq > ? ORDER BY seq LIMIT ?`, jobID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.Stream, &e.Text, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// lastEvents is a job's last n lines, oldest first.
func lastEvents(ctx context.Context, api *plugin.API, jobID int64, n int) ([]Event, error) {
	rows, err := api.Query(ctx, `SELECT seq, stream, text, at FROM runners_events WHERE job_id = ? ORDER BY seq DESC LIMIT ?`, jobID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.Stream, &e.Text, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	slices.Reverse(out)
	return out, rows.Err()
}

// addEvents appends lines to a job's log, numbered after its last, and
// drops the oldest past maxEvents; it answers them numbered.
func addEvents(ctx context.Context, tx plugin.Querier, j *Job, lines []Event) ([]Event, error) {
	seq := j.LastSeq
	for i := range lines {
		seq++
		lines[i].Seq = seq
		if lines[i].At == "" {
			lines[i].At = stamp()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runners_events (job_id, seq, stream, text, at) VALUES (?,?,?,?,?)`, j.ID, seq, lines[i].Stream, lines[i].Text, lines[i].At); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE runners_jobs SET last_seq = ?, updated_at = ? WHERE id = ?`, seq, stamp(), j.ID); err != nil {
		return nil, err
	}
	if seq > maxEvents {
		if _, err := tx.Exec(ctx, `DELETE FROM runners_events WHERE job_id = ? AND seq <= ?`, j.ID, seq-maxEvents); err != nil {
			return nil, err
		}
	}
	j.LastSeq = seq
	return lines, nil
}

// ---- default repositories -------------------------------------------------------------

// Repo is the default repository for a client or an engagement.
type Repo struct {
	ID         int64  `json:"id"`
	Record     string `json:"record"`
	RecordName string `json:"record_name"`
	Repo       string `json:"repo"`
	BaseBranch string `json:"base_branch"`
}

func allRepos(ctx context.Context, api *plugin.API) ([]Repo, error) {
	rows, err := api.Query(ctx, `SELECT id, record, COALESCE(record_name, ''), repo, COALESCE(base_branch, '') FROM runners_repos ORDER BY record_name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repo{}
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.Record, &r.RecordName, &r.Repo, &r.BaseBranch); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// repoFor is the default repository for a record: its engagement's, else
// its client's.
func repoFor(ctx context.Context, api *plugin.API, work map[string]any) (Repo, bool) {
	for _, key := range []string{"engagement", "client"} {
		ref, _ := work[key].(map[string]any)
		rec := str(ref["record"])
		if rec == "" {
			continue
		}
		var r Repo
		err := api.QueryRow(ctx, `SELECT id, record, COALESCE(record_name, ''), repo, COALESCE(base_branch, '') FROM runners_repos WHERE record = ?`, rec).
			Scan(&r.ID, &r.Record, &r.RecordName, &r.Repo, &r.BaseBranch)
		if err == nil {
			return r, true
		}
	}
	return Repo{}, false
}
