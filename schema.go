package runners

// schema is the plugin's tables. Times are UTC ISO 8601 strings, so they
// sort.
//
// runners_machines are the runners people added: each holds one machine
// token (token_id, the core's plugin_machine_tokens), reports its tools,
// versions and the repositories it allows on each heartbeat
// (capabilities), and may be held to fewer repositories from Runwell's side
// (allowed_repos, a JSON list; empty means whatever the runner allows).
//
// runners_jobs are pieces of work handed to a runner: the record they're for
// ("Todo:18"), the repository and base branch, the instructions and the
// work's brief as it was when handed over, who asked (and through which
// agent), the runner that took it, and how it went. A finished job with
// changes waits for a person's approval before Runwell acts on it.
//
// runners_events are a job's log, line by line, at most maxEvents a job.
// runners_repos are the default repository for a client or an engagement.
const schema = `
CREATE TABLE IF NOT EXISTS "runners_machines" ("id" integer PRIMARY KEY AUTOINCREMENT NOT NULL, "name" varchar NOT NULL, "token_id" integer NOT NULL, "status" varchar DEFAULT 'new' NOT NULL, "last_seen_at" varchar, "capabilities" text, "allowed_repos" text, "paused" boolean DEFAULT 0 NOT NULL, "created_by_id" integer, "created_at" varchar NOT NULL, "updated_at" varchar NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS "index_runners_machines_on_token_id" ON "runners_machines" ("token_id");

CREATE TABLE IF NOT EXISTS "runners_jobs" ("id" integer PRIMARY KEY AUTOINCREMENT NOT NULL, "record" varchar NOT NULL, "record_title" varchar, "repo" varchar NOT NULL, "base_branch" varchar, "branch" varchar, "instructions" text NOT NULL, "brief" text, "status" varchar DEFAULT 'queued' NOT NULL, "machine_id" integer, "claimed_by_id" integer, "requested_by_id" integer NOT NULL, "requested_by_name" varchar, "via_agent" varchar, "timeout_minutes" integer DEFAULT 60 NOT NULL, "cancel_requested" boolean DEFAULT 0 NOT NULL, "pr_url" varchar, "summary" text, "files_changed" text, "duration_seconds" integer, "error" text, "decision_note" text, "decided_by_id" integer, "decided_by_name" varchar, "decided_at" varchar, "cleanup" varchar, "retry_of_id" integer, "last_seq" integer DEFAULT 0 NOT NULL, "claimed_at" varchar, "started_at" varchar, "finished_at" varchar, "created_at" varchar NOT NULL, "updated_at" varchar NOT NULL);
CREATE INDEX IF NOT EXISTS "index_runners_jobs_on_status" ON "runners_jobs" ("status");
CREATE INDEX IF NOT EXISTS "index_runners_jobs_on_record" ON "runners_jobs" ("record");

CREATE TABLE IF NOT EXISTS "runners_events" ("id" integer PRIMARY KEY AUTOINCREMENT NOT NULL, "job_id" integer NOT NULL, "seq" integer NOT NULL, "stream" varchar NOT NULL, "text" text NOT NULL, "at" varchar NOT NULL);
CREATE INDEX IF NOT EXISTS "index_runners_events_on_job_id_and_seq" ON "runners_events" ("job_id", "seq");

CREATE TABLE IF NOT EXISTS "runners_repos" ("id" integer PRIMARY KEY AUTOINCREMENT NOT NULL, "record" varchar NOT NULL, "record_name" varchar, "repo" varchar NOT NULL, "base_branch" varchar, "created_at" varchar NOT NULL, "updated_at" varchar NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS "index_runners_repos_on_record" ON "runners_repos" ("record");
`
