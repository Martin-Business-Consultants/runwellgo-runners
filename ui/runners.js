// What the Runners screens share: the plugin's routes, and how a job's
// status and a runner's state look (a word and a tint, never the tint alone).
export const X = "/x/runners";

// The plugin's route, with a query: rx("/jobs", {status: "active"}).
export function rx(path, query = {}) {
  const q = new URLSearchParams(Object.entries(query).filter(([, v]) => v !== "" && v != null && v !== "all")).toString();
  return X + path + (q ? "?" + q : "");
}

const TINTS = {
  slate: "border-transparent bg-muted text-muted-foreground",
  indigo: "border-transparent bg-primary/12 text-primary",
  amber: "border-transparent bg-amber-500/15 text-amber-800 dark:text-amber-300",
  green: "border-transparent bg-emerald-500/15 text-emerald-800 dark:text-emerald-300",
  red: "border-transparent bg-destructive/12 text-destructive",
};

export const JOB_TINT = {
  queued: "slate", claimed: "indigo", running: "indigo", awaiting_approval: "amber",
  approved: "green", succeeded: "green", declined: "slate", cancelled: "slate", failed: "red",
};
export const jobClass = (status) => TINTS[JOB_TINT[status] || "slate"];

export const RUNNER_STATE = {
  online: ["Online", "green"], busy: ["Working", "indigo"], offline: ["Offline", "slate"],
  paused: ["Paused", "amber"], waiting: ["Not connected yet", "slate"],
};
export const runnerLabel = (state) => (RUNNER_STATE[state] || [state])[0];
export const runnerClass = (state) => TINTS[(RUNNER_STATE[state] || [, "slate"])[1]];

export const STATUS_OPTIONS = [
  { value: "all", label: "Any status" },
  { value: "active", label: "Queued or running" },
  { value: "awaiting_approval", label: "Awaiting approval" },
  { value: "done", label: "Approved or done" },
  { value: "failed", label: "Failed" },
  { value: "declined", label: "Declined" },
  { value: "cancelled", label: "Cancelled" },
];

// duration is seconds in words: 45s, 3m 20s, 1h 5m.
export function duration(s) {
  if (!s) return "";
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

// repoName is how a repository reads in a list: owner/name, or a local
// path's last part.
export const repoName = (r) => (r?.startsWith("/") ? "…/" + r.split("/").filter(Boolean).slice(-1)[0] : r);
