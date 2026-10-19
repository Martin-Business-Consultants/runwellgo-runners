package runners

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Martin-Business-Consultants/runwellgo/internal/mail"
	"github.com/Martin-Business-Consultants/runwellgo/internal/plugin"
	"github.com/Martin-Business-Consultants/runwellgo/internal/store"
)

// env is Runwell as the plugin sees it: one piece of work (Todo:18 on
// Engagement:9 for Client:7), one person, the brief_work tool, and a record
// of the writes and notifications.
type env struct {
	mu       sync.Mutex
	writes   []any
	notified []plugin.Notification
}

func (e *env) Setting(context.Context, string) (map[string]any, error)      { return map[string]any{}, nil }
func (e *env) ReplaceSetting(context.Context, string, map[string]any) error { return nil }
func (e *env) Site(context.Context) (plugin.Site, error) {
	return plugin.Site{Name: "Runwell", URL: "https://runwell.test"}, nil
}
func (e *env) SendMail(context.Context, mail.Message) error { return nil }
func (e *env) Records(_ context.Context, q plugin.Query) ([]map[string]any, error) {
	switch q.Type {
	case "Todo":
		if q.ID != 18 {
			return nil, nil
		}
		return []map[string]any{{"type": "Todo", "id": int64(18), "record": "Todo:18", "title": "Build the home page",
			"description": "<p>Use the <strong>new</strong> design.</p>",
			"engagement":  map[string]any{"record": "Engagement:9", "display_name": "P-1 Website"},
			"client":      map[string]any{"record": "Client:7", "display_name": "Harbor Dental"}}}, nil
	case "Engagement":
		return []map[string]any{{"type": "Engagement", "id": int64(9), "record": "Engagement:9", "display_name": "P-1 Website"}}, nil
	case "Client":
		return []map[string]any{{"type": "Client", "id": int64(7), "record": "Client:7", "display_name": "Harbor Dental"}}, nil
	case "User":
		return []map[string]any{{"type": "User", "id": int64(1), "name": "Ted"}}, nil
	}
	return nil, nil
}
func (e *env) Write(_ context.Context, _ plugin.Actor, _ string, w any) (map[string]any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.writes = append(e.writes, w)
	return map[string]any{}, nil
}
func (e *env) Audit(context.Context, plugin.Actor, string, int64, string, map[string]any) {}
func (e *env) AttachFile(context.Context, string, int64, string, string, string, io.Reader) (*plugin.File, error) {
	return nil, errors.New("no")
}
func (e *env) Attachments(context.Context, string, int64, string) ([]plugin.File, error) {
	return nil, nil
}
func (e *env) OpenFile(context.Context, string, int64, int64) (io.ReadCloser, *plugin.File, error) {
	return nil, nil, errors.New("no")
}
func (e *env) DetachFile(context.Context, string, int64, int64) error { return nil }
func (e *env) Tools(context.Context, plugin.Actor) ([]plugin.ToolInfo, error) {
	return []plugin.ToolInfo{{Name: "brief_work", Title: "Get a brief", ReadOnly: true}}, nil
}
func (e *env) RunTool(_ context.Context, _ plugin.Actor, _, name string, args map[string]any) (map[string]any, error) {
	return map[string]any{"status": "ok", "data": map[string]any{"brief": fmt.Sprintf("# Brief for Todo:%v", args["id"])}}, nil
}
func (e *env) Seal(plain string) string                         { return plain }
func (e *env) Unseal(sealed string) (string, bool)              { return sealed, true }
func (e *env) Publish(string, string, []int64, json.RawMessage) {}
func (e *env) Online([]int64) map[int64]bool                    { return map[int64]bool{} }
func (e *env) Notify(_ context.Context, _ string, n plugin.Notification) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notified = append(e.notified, n)
	return nil
}
func (e *env) RichText(html string) plugin.RichText {
	text := html
	for _, t := range []string{"<p>", "</p>", "<strong>", "</strong>"} {
		text = strings.ReplaceAll(text, t, "")
	}
	return plugin.RichText{HTML: html, Text: text}
}

var (
	ted    = &plugin.Caller{ID: 1, Name: "Ted", Role: "owner", Capabilities: []string{capManage, capUse}}
	marcus = &plugin.Caller{ID: 2, Name: "Marcus", Role: "member", Capabilities: []string{capUse}}
)

func setup(t *testing.T) (*plugin.Host, *env) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{}
	spawn := func(ctx context.Context, _ string, fn func(context.Context)) bool { fn(ctx); return true }
	h := plugin.New(e, db, slog.New(slog.NewTextHandler(io.Discard, nil)), spawn, Module())
	if err := h.LoadAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Install(t.Context(), nil, Key); err != nil {
		t.Fatal(err)
	}
	grants := Module().Manifest.Permissions
	on := true
	if _, err := h.Configure(t.Context(), nil, Key, &grants, &on); err != nil {
		t.Fatal(err)
	}
	return h, e
}

// call serves a route's handler as a person (or, with a machine, as that
// machine) and answers its status and JSON.
func call(t *testing.T, h *plugin.Host, who any, method, export string, params map[string]string, body any, query map[string][]string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	if body == nil {
		b = nil
	}
	req := &plugin.Request{Method: method, Params: params, Query: query, Body: b}
	if req.Query == nil {
		req.Query = map[string][]string{}
	}
	if req.Params == nil {
		req.Params = map[string]string{}
	}
	var actor plugin.Actor
	switch w := who.(type) {
	case *plugin.Caller:
		req.Actor, actor = w, "person"
	case *plugin.Machine:
		req.Machine = w
	}
	res, err := h.Serve(t.Context(), Key, export, req, actor)
	if err != nil {
		t.Fatalf("%s: %v", export, err)
	}
	out := map[string]any{}
	_ = json.Unmarshal(res.Body, &out)
	if res.Status == 0 {
		res.Status = 200
	}
	return res.Status, out
}

func id(v any) string { return strconv.FormatInt(intOf(v), 10) }

// addRunner adds a runner and answers the machine its token stands for.
func addRunner(t *testing.T, h *plugin.Host, name string, repos ...string) (*plugin.Machine, string) {
	t.Helper()
	status, out := call(t, h, ted, "POST", "createRunner", nil, map[string]any{"runner": map[string]any{"name": name}}, nil)
	if status != 201 {
		t.Fatalf("createRunner: %d %v", status, out)
	}
	token := str(out["token"])
	m, err := h.MachineFor(t.Context(), Key, token)
	if err != nil || m == nil {
		t.Fatalf("the new runner's token isn't one: %v", err)
	}
	if status, out := call(t, h, m, "POST", "heartbeat", nil, map[string]any{"allowed_repos": repos, "tools": map[string]any{"git": "2.4"}}, nil); status != 200 {
		t.Fatalf("heartbeat: %d %v", status, out)
	}
	return m, token
}

func TestTokensAreThePluginsOwnAndRevocable(t *testing.T) {
	h, _ := setup(t)
	m, token := addRunner(t, h, "Box", "acme/site")
	if got, _ := h.MachineFor(t.Context(), "chat", token); got != nil {
		t.Fatal("another plugin's routes took the runner's token")
	}
	if got, _ := h.MachineFor(t.Context(), Key, "rwm_made-up"); got != nil {
		t.Fatal("a made-up token was taken")
	}
	if got, _ := h.MachineFor(t.Context(), Key, strings.TrimPrefix(token, "rwm_")); got != nil {
		t.Fatal("a token without its prefix was taken")
	}
	var runnerID int64
	_, list := call(t, h, ted, "GET", "listRunners", nil, nil, nil)
	for _, r := range list["runners"].([]any) {
		runnerID = intOf(r.(map[string]any)["id"])
	}
	if status, _ := call(t, h, marcus, "DELETE", "deleteRunner", map[string]string{"id": strconv.FormatInt(runnerID, 10)}, nil, nil); status != 200 {
		t.Fatalf("deleteRunner: %d", status)
	}
	if got, _ := h.MachineFor(t.Context(), Key, token); got != nil {
		t.Fatal("a removed runner's token still works")
	}
	if status, _ := call(t, h, m, "POST", "heartbeat", nil, map[string]any{}, nil); status != 401 {
		t.Fatalf("a removed runner's heartbeat answered %d", status)
	}
}

func TestAMachineRouteNeedsTheGrantAndCantBeATool(t *testing.T) {
	m := Module()
	m.Manifest.Permissions.Machines = false
	if err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		plugin.New(&env{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, m)
		return nil
	}(); err == nil || !strings.Contains(err.Error(), "needs permissions.machines") {
		t.Fatalf("a machine route without the grant: %v", err)
	}
}

func TestAJobFromHandOffToApproval(t *testing.T) {
	h, e := setup(t)
	box, _ := addRunner(t, h, "Box", "acme/*")
	other, _ := addRunner(t, h, "Other", "elsewhere/repo")

	// No repository and no default: it says how to fix it.
	status, out := call(t, h, marcus, "POST", "createJob", nil, map[string]any{"record": "Todo:18", "instructions": "Do it."}, nil)
	if status != 422 || !strings.Contains(fmt.Sprint(out["errors"]), "default") {
		t.Fatalf("no repository: %d %v", status, out)
	}
	// A default for the engagement fills it in.
	if status, out := call(t, h, ted, "PUT", "saveRepo", nil, map[string]any{"repo": map[string]any{"record": "Engagement:9", "repo": "https://github.com/Acme/Site.git", "base_branch": "main"}}, nil); status != 200 {
		t.Fatalf("saveRepo: %d %v", status, out)
	}
	_, form := call(t, h, marcus, "GET", "handoffForm", nil, nil, map[string][]string{"record": {"Todo:18"}})
	values := form["handoff"].(map[string]any)
	if values["repo"] != "acme/site" || values["instructions"] != "Build the home page\n\nUse the new design." {
		t.Fatalf("the hand-off form starts with %v", values)
	}
	status, out = call(t, h, marcus, "POST", "createJob", nil, map[string]any{"job": map[string]any{"record": "Todo:18", "instructions": "Build it."}}, nil)
	if status != 201 {
		t.Fatalf("createJob: %d %v", status, out)
	}
	job := out["job"].(map[string]any)
	jobID := id(job["id"])
	if job["repo"] != "acme/site" || job["branch"] != "runwell/"+jobID+"-build-the-home-page" || job["status"] != statusQueued {
		t.Fatalf("the job: %v", job)
	}

	// A runner that doesn't allow the repository never gets it; one that
	// does gets it once.
	if _, out := call(t, h, other, "POST", "claim", nil, map[string]any{}, nil); out["job"] != nil {
		t.Fatalf("a runner that doesn't allow acme/site got %v", out["job"])
	}
	_, out = call(t, h, box, "POST", "claim", nil, map[string]any{}, nil)
	claimed, _ := out["job"].(map[string]any)
	if claimed == nil || claimed["brief"] != "# Brief for Todo:18" || claimed["base_branch"] != "main" {
		t.Fatalf("claim: %v", out)
	}
	if _, out := call(t, h, box, "POST", "claim", nil, map[string]any{}, nil); out["job"] != nil {
		t.Fatal("the same job was handed out twice")
	}
	if status, _ := call(t, h, other, "POST", "postEvents", map[string]string{"job_id": jobID}, map[string]any{"events": []any{}}, nil); status != 404 {
		t.Fatalf("another runner reached the job: %d", status)
	}

	// Its log, secrets taken out; the first lines make it running.
	_, out = call(t, h, box, "POST", "postEvents", map[string]string{"job_id": jobID}, map[string]any{"events": []any{
		map[string]any{"stream": "stdout", "text": "working"}, map[string]any{"stream": "stderr", "text": "token=ghp_abcdefghijklmnopqrstuvwxyz0123"},
	}}, nil)
	if out["cancel"] != false || out["status"] != statusRunning {
		t.Fatalf("events: %v", out)
	}
	_, out = call(t, h, marcus, "GET", "showJob", map[string]string{"job_id": jobID}, nil, nil)
	log := fmt.Sprint(out["events"])
	if !strings.Contains(log, "working") || strings.Contains(log, "ghp_") {
		t.Fatalf("the log: %s", log)
	}

	// Finished with changes: it waits for approval, and says so.
	_, out = call(t, h, box, "POST", "finish", map[string]string{"job_id": jobID}, map[string]any{"outcome": "changes", "pr_url": "https://github.com/acme/site/pull/4",
		"summary": "Built it.", "files_changed": []string{"index.html"}, "duration_seconds": 42}, nil)
	if out["status"] != statusAwaiting {
		t.Fatalf("finish: %v", out)
	}
	if len(e.notified) == 0 || e.notified[len(e.notified)-1].UserID != marcus.ID || !strings.Contains(e.notified[len(e.notified)-1].Title, "approve") {
		t.Fatalf("notified: %v", e.notified)
	}
	// Someone else (not managing runners) can't decide; the person who asked can.
	if status, _ := call(t, h, &plugin.Caller{ID: 3, Name: "Sam", Capabilities: []string{capUse}}, "POST", "approveJob", map[string]string{"job_id": jobID}, map[string]any{}, nil); status != 403 {
		t.Fatalf("someone else approved: %d", status)
	}
	if status, out := call(t, h, marcus, "POST", "approveJob", map[string]string{"job_id": jobID}, map[string]any{"note": "Good"}, nil); status != 200 || out["job"].(map[string]any)["status"] != statusApproved {
		t.Fatalf("approve: %d %v", status, out)
	}
	var note plugin.Note
	var change plugin.WorkChange
	for _, w := range e.writes {
		switch x := w.(type) {
		case plugin.Note:
			note = x
		case plugin.WorkChange:
			change = x
		}
	}
	if note.Record != "Todo:18" || !strings.Contains(note.Body, "https://github.com/acme/site/pull/4") || change.ID != 18 || change.Status == nil || *change.Status != "in_review" {
		t.Fatalf("approving wrote %+v and %+v", note, change)
	}
	if status, _ := call(t, h, marcus, "POST", "approveJob", map[string]string{"job_id": jobID}, map[string]any{}, nil); status != 409 {
		t.Fatalf("approved twice: %d", status)
	}
}

func TestDeclineAsksTheRunnerToCleanUp(t *testing.T) {
	h, _ := setup(t)
	box, _ := addRunner(t, h, "Box", "acme/site")
	_, out := call(t, h, ted, "POST", "createJob", nil, map[string]any{"record": "Todo:18", "repo": "acme/site", "instructions": "Go."}, nil)
	jobID := id(out["job"].(map[string]any)["id"])
	call(t, h, box, "POST", "claim", nil, map[string]any{}, nil)
	call(t, h, box, "POST", "finish", map[string]string{"job_id": jobID}, map[string]any{"outcome": "changes", "pr_url": "https://github.com/acme/site/pull/9", "files_changed": []string{"a"}}, nil)
	if status, out := call(t, h, ted, "POST", "declineJob", map[string]string{"job_id": jobID}, map[string]any{"note": "No"}, nil); status != 200 || out["job"].(map[string]any)["cleanup"] != "pending" {
		t.Fatalf("decline: %d %v", status, out)
	}
	_, beat := call(t, h, box, "POST", "heartbeat", nil, map[string]any{"allowed_repos": []string{"acme/site"}}, nil)
	cleanup := beat["cleanup"].([]any)
	if len(cleanup) != 1 || cleanup[0].(map[string]any)["pr_url"] != "https://github.com/acme/site/pull/9" {
		t.Fatalf("the heartbeat's cleanup: %v", beat)
	}
	call(t, h, box, "POST", "cleaned", map[string]string{"job_id": jobID}, map[string]any{}, nil)
	if _, beat := call(t, h, box, "POST", "heartbeat", nil, map[string]any{"allowed_repos": []string{"acme/site"}}, nil); len(beat["cleanup"].([]any)) != 0 {
		t.Fatal("cleaned up twice")
	}
}

func TestCancelReachesTheRunner(t *testing.T) {
	h, _ := setup(t)
	box, _ := addRunner(t, h, "Box", "acme/site")
	_, out := call(t, h, ted, "POST", "createJob", nil, map[string]any{"record": "Todo:18", "repo": "acme/site", "instructions": "Go."}, nil)
	jobID := id(out["job"].(map[string]any)["id"])
	call(t, h, box, "POST", "claim", nil, map[string]any{}, nil)
	if status, _ := call(t, h, marcus, "POST", "cancelJob", map[string]string{"job_id": jobID}, nil, nil); status != 403 {
		t.Fatalf("someone who didn't ask cancelled it: %d", status)
	}
	if status, _ := call(t, h, ted, "POST", "cancelJob", map[string]string{"job_id": jobID}, nil, nil); status != 200 {
		t.Fatalf("cancel: %d", status)
	}
	if _, out := call(t, h, box, "GET", "checkCancel", map[string]string{"job_id": jobID}, nil, nil); out["cancel"] != true {
		t.Fatalf("the runner isn't told to stop: %v", out)
	}
	if _, out := call(t, h, box, "POST", "finish", map[string]string{"job_id": jobID}, map[string]any{"outcome": "cancelled"}, nil); out["status"] != statusCancelled {
		t.Fatalf("finish after cancel: %v", out)
	}
}

func TestStuckJobsFail(t *testing.T) {
	h, e := setup(t)
	box, _ := addRunner(t, h, "Box", "acme/site")
	_, out := call(t, h, ted, "POST", "createJob", nil, map[string]any{"record": "Todo:18", "repo": "acme/site", "instructions": "Go.", "timeout_minutes": 5}, nil)
	jobID := id(out["job"].(map[string]any)["id"])
	call(t, h, box, "POST", "claim", nil, map[string]any{}, nil)
	check := func(want string) {
		t.Helper()
		_, out := call(t, h, ted, "GET", "showJob", map[string]string{"job_id": jobID}, nil, nil)
		if got := out["job"].(map[string]any)["status"]; got != want {
			t.Fatalf("status %v, want %s", got, want)
		}
	}
	sweepAt(t, h, time.Now().Add(2*time.Minute))
	check(statusClaimed)
	sweepAt(t, h, time.Now().Add(7*time.Minute))
	check(statusFailed)
	if n := e.notified[len(e.notified)-1]; !strings.Contains(n.Title, "failed") {
		t.Fatalf("notified: %+v", n)
	}
}

// sweepAt runs the stuck-jobs check as if it were at.
func sweepAt(t *testing.T, h *plugin.Host, at time.Time) {
	t.Helper()
	sweepClock = func() time.Time { return at }
	defer func() { sweepClock = time.Now }()
	// The task, through the host, as the scheduler runs it.
	h.RunTasks(t.Context(), at, time.UTC)
}

func TestRepos(t *testing.T) {
	for in, want := range map[string]string{"Acme/Site": "acme/site", "https://github.com/acme/site.git": "acme/site", "git@github.com:acme/site.git": "acme/site", "/srv/Git/site.git/": "/srv/Git/site.git"} {
		if got := normalRepo(in); got != want {
			t.Errorf("normalRepo(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"acme", "acme/site/x", "../etc", "acme/si te", "acme/$(x)"} {
		if validRepo(bad) {
			t.Errorf("%q passed as a repository", bad)
		}
	}
	if !matchesAny([]string{"acme/*"}, "Acme/site") || matchesAny([]string{"acme/*"}, "acmex/site") || matchesAny([]string{"acme/site"}, "acme/site2") {
		t.Fatal("allowlists match wrong")
	}
	if got := redact("key: sk-ant-abcdefghijklmnop and API_KEY=hunter2hunter2"); strings.Contains(got, "sk-ant") || strings.Contains(got, "hunter2") {
		t.Fatalf("redact left %q", got)
	}
}
