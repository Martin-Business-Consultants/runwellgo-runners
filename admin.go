package runners

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/runwellgo/internal/plugin"
)

// Settings › Runners: the machines work can go to (add one and get its
// token once, pause it, remove it, hold it to some repositories), and the
// default repository for each client or engagement.

// activeCounts is how many jobs each runner has on now.
func activeCounts(ctx context.Context, api *plugin.API) (map[int64]int, error) {
	rows, err := api.Query(ctx, `SELECT claimed_by_id, count(*) FROM runners_jobs WHERE status IN (?,?) AND claimed_by_id IS NOT NULL GROUP BY claimed_by_id`, statusClaimed, statusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func listRunners(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	runners, err := allRunners(ctx, api)
	if err != nil {
		return nil, err
	}
	counts, err := activeCounts(ctx, api)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	list, online := []map[string]any{}, 0
	for _, m := range runners {
		if m.Online(now) && !m.Paused {
			online++
		}
		list = append(list, runnerJSON(m, now, counts[m.ID]))
	}
	repos, err := allRepos(ctx, api)
	if err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("%d runners, %d online", len(list), online)
	if len(list) == 0 {
		summary = "No runners yet: someone who manages runners adds one in Settings › Runners."
	}
	site, _ := api.Site(ctx)
	return ok(map[string]any{"summary": summary, "runners": list, "repos": repos, "can_manage": req.Actor.Can(capManage), "url": site.URL})
}

func runnerFor(ctx context.Context, api *plugin.API, req *plugin.Request) (*Runner, *plugin.Response, error) {
	m, err := findRunner(ctx, api, pathID(req, "id"))
	if errors.Is(err, errNotFound) {
		return nil, refuse(http.StatusNotFound, "Couldn't find that runner."), nil
	}
	return m, nil, err
}

func showRunner(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	m, res, err := runnerFor(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	counts, err := activeCounts(ctx, api)
	if err != nil {
		return nil, err
	}
	return ok(map[string]any{"runner": runnerJSON(m, time.Now(), counts[m.ID])})
}

// commands are what to run on the machine: install the runner, set it up
// with its token, and run it.
func commands(site plugin.Site, token string, repos []string) map[string]any {
	url := strings.TrimSuffix(site.URL, "/")
	repoFlag := ""
	if len(repos) > 0 {
		repoFlag = " --repos " + strings.Join(repos, ",")
	} else {
		repoFlag = " --repos owner/name"
	}
	return map[string]any{
		"install": "curl -fsSL https://raw.githubusercontent.com/Martin-Business-Consultants/runwellgo/main/install.sh | RUNWELL_BINARY=runwell-runner sh",
		"init":    fmt.Sprintf("runwell-runner init --url %s --token %s%s", url, token, repoFlag),
		"doctor":  "runwell-runner doctor",
		"run":     "runwell-runner run",
	}
}

func createRunner(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	c, res := caller(req)
	if res != nil {
		return res, nil
	}
	p := wrapped(params(req), "runner")
	name := strings.TrimSpace(str(p["name"]))
	if name == "" {
		return invalid("name", "can't be blank"), nil
	}
	if len(name) > 80 {
		return invalid("name", "is too long (80 characters at most)"), nil
	}
	repos := []string{}
	for _, r := range strList(p["allowed_repos"]) {
		if r = normalRepo(r); !validPattern(r) {
			return invalid("allowed_repos", r+" isn't owner/name (or owner/*)"), nil
		}
		repos = append(repos, r)
	}
	token, tokenID, err := api.IssueToken(ctx, "Runner: "+name)
	if err != nil {
		return nil, err
	}
	now := stamp()
	reposJSON, _ := json.Marshal(repos)
	r, err := api.Exec(ctx, `INSERT INTO runners_machines (name, token_id, status, allowed_repos, created_by_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		name, tokenID, "new", string(reposJSON), c.ID, now, now)
	if err != nil {
		_ = api.RevokeToken(ctx, tokenID)
		return nil, err
	}
	id, _ := r.LastInsertId()
	m, err := findRunner(ctx, api, id)
	if err != nil {
		return nil, err
	}
	site, _ := api.Site(ctx)
	publishRunner(ctx, api, m)
	msg := "Added " + name + ". Copy its token now: it isn't shown again."
	return plugin.JSON(http.StatusCreated, map[string]any{"summary": msg, "message": msg, "runner": runnerJSON(m, time.Now(), 0),
		"token": token, "commands": commands(site, token, repos)}), nil
}

func updateRunner(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	m, res, err := runnerFor(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	p := wrapped(params(req), "runner")
	if v, ok := p["name"]; ok {
		name := strings.TrimSpace(str(v))
		if name == "" {
			return invalid("name", "can't be blank"), nil
		}
		m.Name = name
	}
	if v, ok := p["paused"]; ok {
		m.Paused = v == true || str(v) == "true" || str(v) == "1"
	}
	if v, ok := p["allowed_repos"]; ok {
		repos := []string{}
		for _, r := range strList(v) {
			if r = normalRepo(r); !validPattern(r) {
				return invalid("allowed_repos", r+" isn't owner/name (or owner/*)"), nil
			}
			repos = append(repos, r)
		}
		m.AllowedRepos = repos
	}
	reposJSON, _ := json.Marshal(m.AllowedRepos)
	if _, err := api.Exec(ctx, `UPDATE runners_machines SET name = ?, paused = ?, allowed_repos = ?, updated_at = ? WHERE id = ?`, m.Name, m.Paused, string(reposJSON), stamp(), m.ID); err != nil {
		return nil, err
	}
	publishRunner(ctx, api, m)
	counts, _ := activeCounts(ctx, api)
	msg := "Saved " + m.Name + "."
	if _, ok := p["paused"]; ok {
		msg = map[bool]string{true: "Paused " + m.Name + ": it finishes what it's on and takes nothing new.", false: m.Name + " takes work again."}[m.Paused]
	}
	return ok(map[string]any{"summary": msg, "message": msg, "runner": runnerJSON(m, time.Now(), counts[m.ID])})
}

func deleteRunner(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	m, res, err := runnerFor(ctx, api, req)
	if res != nil || err != nil {
		return res, err
	}
	if err := api.RevokeToken(ctx, m.TokenID); err != nil {
		return nil, err
	}
	if _, err := api.Exec(ctx, `DELETE FROM runners_machines WHERE id = ?`, m.ID); err != nil {
		return nil, err
	}
	// Jobs waiting for this runner alone wait for any.
	if _, err := api.Exec(ctx, `UPDATE runners_jobs SET machine_id = NULL WHERE machine_id = ? AND status = ?`, m.ID, statusQueued); err != nil {
		return nil, err
	}
	publishRunner(ctx, api, m)
	msg := "Removed " + m.Name + ". Its token no longer works."
	return ok(map[string]any{"summary": msg, "message": msg})
}

// ---- default repositories --------------------------------------------------------------

func listRepos(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	repos, err := allRepos(ctx, api)
	if err != nil {
		return nil, err
	}
	options := []map[string]any{}
	clients, err := api.Records(ctx, plugin.Query{Type: "Client", Limit: 500})
	if err != nil {
		return nil, err
	}
	for _, c := range clients {
		options = append(options, map[string]any{"value": str(c["record"]), "label": str(c["display_name"]), "hint": "Client"})
	}
	engagements, err := api.Records(ctx, plugin.Query{Type: "Engagement", Open: true, Limit: 500})
	if err != nil {
		return nil, err
	}
	for _, e := range engagements {
		options = append(options, map[string]any{"value": str(e["record"]), "label": str(e["display_name"]), "hint": "Engagement"})
	}
	known, err := knownRepos(ctx, api)
	if err != nil {
		return nil, err
	}
	return ok(map[string]any{"repos": repos, "records": options, "known": known})
}

func saveRepo(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	p := wrapped(params(req), "repo")
	record := strings.TrimSpace(str(p["record"]))
	repo := normalRepo(str(p["repo"]))
	base := strings.TrimSpace(str(p["base_branch"]))
	errs := map[string][]string{}
	typ, _, err := plugin.ParseRecord(record)
	var rec map[string]any
	if err != nil || (typ != "Client" && typ != "Engagement") {
		errs["record"] = []string{"must be a client or an engagement"}
	} else if rec, err = api.Record(ctx, record); errors.Is(err, plugin.ErrNoRecord) {
		errs["record"] = []string{"isn't there"}
	} else if err != nil {
		return nil, err
	}
	if repo == "" {
		errs["repo"] = []string{"can't be blank"}
	} else if !validRepo(repo) {
		errs["repo"] = []string{"must be owner/name, like acme/website"}
	}
	if strings.ContainsAny(base, " \t\n~^:?*[\\") {
		errs["base_branch"] = []string{"isn't a branch name"}
	}
	if len(errs) > 0 {
		return plugin.Invalid(errs), nil
	}
	now := stamp()
	if _, err := api.Exec(ctx, `INSERT INTO runners_repos (record, record_name, repo, base_branch, created_at, updated_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT (record) DO UPDATE SET record_name = excluded.record_name, repo = excluded.repo, base_branch = excluded.base_branch, updated_at = excluded.updated_at`,
		record, str(rec["display_name"]), repo, nullable(base), now, now); err != nil {
		return nil, err
	}
	repos, err := allRepos(ctx, api)
	if err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("%s's work goes to %s.", str(rec["display_name"]), repo)
	return ok(map[string]any{"summary": msg, "message": msg, "repos": repos})
}

func deleteRepo(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	if _, err := api.Exec(ctx, `DELETE FROM runners_repos WHERE id = ?`, pathID(req, "id")); err != nil {
		return nil, err
	}
	repos, err := allRepos(ctx, api)
	if err != nil {
		return nil, err
	}
	return ok(map[string]any{"message": "Removed the default.", "repos": repos})
}
