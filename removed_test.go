package runners

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Martin-Business-Consultants/runwellgo/internal/plugin"
	"github.com/Martin-Business-Consultants/runwellgo/internal/store"
)

// A removed person's jobs and runners are their successor's; who asked and
// who decided stay as names.
func TestUserRemovedHandsJobsAndRunnersOver(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	spawn := func(ctx context.Context, _ string, fn func(context.Context)) bool { fn(ctx); return true }
	h := plugin.New(&env{}, db, slog.New(slog.NewTextHandler(io.Discard, nil)), spawn, Module())
	if err := h.LoadAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Install(t.Context(), nil, Key); err != nil {
		t.Fatal(err)
	}
	grants, on := Module().Manifest.Permissions, true
	if _, err := h.Configure(t.Context(), nil, Key, &grants, &on); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO runners_machines (id, name, token_id, created_by_id, created_at, updated_at) VALUES (1, 'Box', 1, 2, 'now', 'now'), (2, 'Other', 2, 1, 'now', 'now')`,
		`INSERT INTO runners_jobs (id, record, repo, instructions, requested_by_id, requested_by_name, status, created_at, updated_at) VALUES
			(1, 'Todo:1', 'a/b', 'do it', 2, 'Sarah', 'awaiting_approval', 'now', 'now'),
			(2, 'Todo:1', 'a/b', 'do it', 2, NULL, 'done', 'now', 'now'),
			(3, 'Todo:1', 'a/b', 'do it', 1, 'Ted', 'done', 'now', 'now')`,
		`UPDATE runners_jobs SET decided_by_id = 2, decided_by_name = 'Sarah' WHERE id = 3`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		h.Emit(ctx, "user.removed", map[string]any{"subject_type": "User", "subject_id": 2, "kind": "user.removed",
			"payload": map[string]any{"id": 2, "name": "Sarah Lee", "successor_id": 3}})
	}
	rows, err := db.QueryContext(ctx, `SELECT requested_by_id, COALESCE(requested_by_name, ''), COALESCE(decided_by_id, 0), COALESCE(decided_by_name, '') FROM runners_jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := [][4]any{{int64(3), "Sarah", int64(0), ""}, {int64(3), "Sarah Lee", int64(0), ""}, {int64(1), "Ted", int64(0), "Sarah"}}
	i := 0
	for rows.Next() {
		var req, dec int64
		var reqName, decName string
		_ = rows.Scan(&req, &reqName, &dec, &decName)
		if got := [4]any{req, reqName, dec, decName}; got != want[i] {
			t.Fatalf("job %d: %v, want %v", i+1, got, want[i])
		}
		i++
	}
	var created int64
	_ = db.QueryRowContext(ctx, `SELECT created_by_id FROM runners_machines WHERE id = 1`).Scan(&created)
	if created != 3 {
		t.Fatalf("Sarah's runner is %d's, want Marcus's", created)
	}
	// The decision still names who made it.
	if by := decidedBy(&Job{DecidedByName: "Sarah"}); by == nil || by.(map[string]any)["name"] != "Sarah" {
		t.Fatalf("decided_by: %v", by)
	}
	// Marcus may now decide on the job Sarah asked for.
	marcus := &plugin.Caller{ID: 3, Name: "Marcus", Role: "member", Capabilities: []string{capUse}}
	status, out := call(t, h, marcus, "GET", "showJob", map[string]string{"job_id": "1"}, nil, nil)
	job, _ := out["job"].(map[string]any)
	if job == nil {
		job = out
	}
	if status != 200 || job["can_decide"] != true {
		t.Fatalf("showJob as Marcus: %d %v", status, out)
	}
}
