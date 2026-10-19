<script>
  // A runner job: what it's for and where it stands, its log as it runs
  // (live), and what it came back with: the pull request, the agent's
  // summary and the files it changed. Finished with changes, it waits for
  // the person who asked to approve or decline it (here, or in Approvals);
  // a job can be cancelled while it's waiting or running, and tried again
  // once it's over.
  import * as Card from "$lib/components/ui/card/index.js";
  import * as Alert from "$lib/components/ui/alert/index.js";
  import * as Dialog from "$lib/components/ui/dialog/index.js";
  import * as Field from "$lib/components/ui/field/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Badge } from "$lib/components/ui/badge/index.js";
  import { Skeleton } from "$lib/components/ui/skeleton/index.js";
  import { Textarea } from "$lib/components/ui/textarea/index.js";
  import ExternalLink from "@lucide/svelte/icons/external-link";
  import GitPullRequest from "@lucide/svelte/icons/git-pull-request";
  import RotateCcw from "@lucide/svelte/icons/rotate-ccw";
  import Square from "@lucide/svelte/icons/square";
  import Check from "@lucide/svelte/icons/check";
  import X from "@lucide/svelte/icons/x";
  import Time from "$lib/components/Time.svelte";
  import { onDestroy } from "svelte";
  import { get, post } from "$lib/api.js";
  import { page } from "$lib/page.svelte.js";
  import { session } from "$lib/session.svelte.js";
  import { go } from "$lib/router.svelte.js";
  import { subscribe } from "$lib/live.svelte.js";
  import { toast, toastError } from "$lib/toast.svelte.js";
  import { confirm } from "$lib/confirm.svelte.js";
  import NotFound from "../../routes/NotFound.svelte";
  import JobLog from "./JobLog.svelte";
  import { rx, jobClass, duration } from "./runners.js";

  let { params } = $props();
  let job = $state(null), events = $state([]), missing = $state(false), busy = $state(false);
  let deciding = $state(null), note = $state("");
  let lastSeq = 0;

  function show(j) {
    job = j;
    page.set(`Job ${j.id}`, [{ label: "Runners", href: "/runners" }]);
  }
  async function load() {
    try {
      const res = await get(rx(`/jobs/${params.id}`));
      show(res.job);
      events = res.events;
      lastSeq = res.last_seq;
    } catch (e) { if (e.status === 404) missing = true; else toastError(e); }
  }
  load();

  // Lines missed (a gap in the numbers): read them from the route.
  async function catchUp() {
    try {
      const res = await get(rx(`/jobs/${params.id}/events`, { after: lastSeq }));
      add(res.events);
    } catch {}
  }
  function add(list) {
    const fresh = list.filter((e) => e.seq > lastSeq);
    if (!fresh.length) return;
    events = [...events, ...fresh].slice(-5000);
    lastSeq = fresh[fresh.length - 1].seq;
  }
  const stop = subscribe("runners", (topic, data) => {
    if (topic === "resync") return load();
    if (!job) return;
    if (topic === "events" && data.job_id === job.id) {
      if (data.events.length && (data.events[0].seq > lastSeq + 1 || data.more)) catchUp();
      else add(data.events);
    } else if (topic === "job" && data.id === job.id && data.status !== job.status) {
      load();
    }
  });
  onDestroy(stop);

  async function act(path, body, message) {
    busy = true;
    try {
      const res = await post(rx(`/jobs/${job.id}/${path}`), body);
      toast(res.message || message);
      if (res.job && res.job.id !== job.id) go(`/runners/jobs/${res.job.id}`);
      else await load();
    } catch (e) { toastError(e); }
    busy = false;
  }
  async function cancel() {
    const running = job.status === "running" || job.status === "claimed";
    if (!(await confirm({ title: "Cancel this job?", message: running ? "The runner stops the agent within a few seconds. Anything it changed is thrown away." : job.status === "awaiting_approval" ? "Its pull request is closed and its branch deleted." : "It won't be picked up.", confirmLabel: "Cancel job", destructive: true }))) return;
    act("cancel", {}, "Cancelled.");
  }
  function decide(kind) { deciding = kind; note = ""; }
  async function submitDecision(e) {
    e.preventDefault();
    const kind = deciding;
    deciding = null;
    await act(kind, { note }, kind === "approve" ? "Approved." : "Declined.");
  }
  const workLabel = $derived(job ? (job.record_title || job.record) : "");
</script>

{#if missing}
  <NotFound title="Couldn’t find that job" back={{ href: "/runners", label: "All jobs" }} />
{:else if !job}
  <Skeleton class="h-64" />
{:else}
  <div class="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
    <div class="min-w-0">
      <h1 class="text-2xl font-semibold tracking-tight">{workLabel}</h1>
      <p class="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <Badge variant="outline" class={jobClass(job.status)}>{job.status_label}</Badge>
        <span>Job {job.id} on <span class="text-foreground break-all">{job.repo}</span></span>
      </p>
    </div>
    <div class="flex shrink-0 flex-wrap gap-2">
      {#if job.pr_url}<Button href={job.pr_url} target="_blank" rel="noopener" variant="outline" size="sm"><GitPullRequest />Pull request</Button>{/if}
      {#if job.can_cancel}<Button variant="outline" size="sm" disabled={busy} onclick={cancel}><Square />Cancel</Button>{/if}
      {#if job.can_retry}<Button variant="outline" size="sm" disabled={busy} onclick={() => act("retry", {}, "Trying again.")}><RotateCcw />Try again</Button>{/if}
    </div>
  </div>

  {#if job.status === "awaiting_approval"}
    <Alert.Root class="border-amber-500/40">
      <GitPullRequest />
      <Alert.Title>Waiting for {job.requested_by.id === session.me.person_id ? "your" : job.requested_by.name + "’s"} approval</Alert.Title>
      <Alert.Description>
        <p>The runner changed {job.files_changed.length} {job.files_changed.length === 1 ? "file" : "files"}. Approving adds a note to the {session.term("work").toLowerCase()} with the pull request and moves it to in review; declining closes the pull request and deletes the branch.</p>
        {#if job.can_approve}
          <div class="mt-2 flex flex-wrap gap-2">
            <Button size="sm" disabled={busy} onclick={() => decide("approve")}><Check />Approve</Button>
            <Button size="sm" variant="outline" disabled={busy} onclick={() => decide("decline")}><X />Decline</Button>
          </div>
        {/if}
      </Alert.Description>
    </Alert.Root>
  {/if}

  <div class="grid gap-4 lg:grid-cols-3">
    <div class="flex min-w-0 flex-col gap-4 lg:col-span-2">
      {#if job.status === "failed" && job.error}
        <Alert.Root variant="destructive"><Alert.Title>It failed</Alert.Title><Alert.Description>{job.error}</Alert.Description></Alert.Root>
      {/if}
      {#if job.summary}
        <Card.Root>
          <Card.Header><Card.Title>What the agent says it did</Card.Title></Card.Header>
          <Card.Content><p class="text-sm whitespace-pre-wrap">{job.summary}</p></Card.Content>
        </Card.Root>
      {/if}
      <Card.Root>
        <Card.Header>
          <Card.Title>Log</Card.Title>
          {#if job.active}<Card.Description>{job.status === "queued" ? (job.waiting?.for_runner ? `No runner that allows ${job.repo} is online: it starts when one is. Check the repositories the runner allows, here and on its machine.` : "Waiting for a runner to pick it up.") : "Live: new lines appear as the runner sends them."}</Card.Description>{/if}
        </Card.Header>
        <Card.Content><JobLog {events} live={job.active} /></Card.Content>
      </Card.Root>
      {#if job.files_changed.length}
        <Card.Root>
          <Card.Header><Card.Title>Files changed <span class="text-muted-foreground font-normal tabular-nums">{job.files_changed.length}</span></Card.Title></Card.Header>
          <Card.Content>
            <ul class="flex flex-col gap-1 font-mono text-xs">
              {#each job.files_changed.slice(0, 200) as f (f)}<li class="break-all">{f}</li>{/each}
            </ul>
          </Card.Content>
        </Card.Root>
      {/if}
    </div>

    <div class="flex min-w-0 flex-col gap-4">
      <Card.Root>
        <Card.Header><Card.Title>Details</Card.Title></Card.Header>
        <Card.Content>
          <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <dt class="text-muted-foreground">{session.term("work")}</dt>
            <dd class="min-w-0">{#if job.record_path}<a class="hover:underline" href={job.record_path}>{workLabel}</a>{:else}{workLabel}{/if}</dd>
            <dt class="text-muted-foreground">Branch</dt>
            <dd class="min-w-0 font-mono text-xs break-all">{job.branch || "—"}{#if job.base_branch}<span class="text-muted-foreground"> from {job.base_branch}</span>{/if}</dd>
            <dt class="text-muted-foreground">Runner</dt>
            <dd>{job.runner?.name || (job.asked_runner ? job.asked_runner.name + " (asked for)" : "Any")}</dd>
            <dt class="text-muted-foreground">Asked by</dt>
            <dd>{job.requested_by.name}{#if job.via_agent}<span class="text-muted-foreground"> via {job.via_agent}</span>{/if}</dd>
            <dt class="text-muted-foreground">Asked</dt>
            <dd><Time value={job.created_at} /></dd>
            {#if job.started_at}<dt class="text-muted-foreground">Started</dt><dd><Time value={job.started_at} /></dd>{/if}
            {#if job.duration_seconds}<dt class="text-muted-foreground">Took</dt><dd class="tabular-nums">{duration(job.duration_seconds)}</dd>{/if}
            <dt class="text-muted-foreground">Time limit</dt>
            <dd class="tabular-nums">{job.timeout_minutes} min</dd>
            {#if job.decided_by}
              <dt class="text-muted-foreground">{job.status === "approved" ? "Approved by" : "Declined by"}</dt>
              <dd>{job.decided_by.name}{#if job.decision_note}<span class="text-muted-foreground block">“{job.decision_note}”</span>{/if}</dd>
            {/if}
            {#if job.cleanup}
              <dt class="text-muted-foreground">Clean-up</dt>
              <dd>{job.cleanup === "pending" ? "Waiting for the runner" : job.cleanup === "done" ? "Branch removed" : "Couldn’t remove the branch"}</dd>
            {/if}
            {#if job.retry_of_id}<dt class="text-muted-foreground">Tries again</dt><dd><a class="hover:underline" href={`/runners/jobs/${job.retry_of_id}`}>Job {job.retry_of_id}</a></dd>{/if}
          </dl>
          {#if job.pr_url}
            <a href={job.pr_url} target="_blank" rel="noopener" class="text-primary mt-3 inline-flex items-center gap-1 text-sm break-all hover:underline">{job.pr_url}<ExternalLink class="size-3.5 shrink-0" /></a>
          {/if}
        </Card.Content>
      </Card.Root>
      <Card.Root>
        <Card.Header><Card.Title>Instructions</Card.Title></Card.Header>
        <Card.Content><p class="text-sm whitespace-pre-wrap">{job.instructions}</p></Card.Content>
      </Card.Root>
    </div>
  </div>

  <Dialog.Root open={!!deciding} onOpenChange={(v) => { if (!v) deciding = null; }}>
    <Dialog.Content>
      <form onsubmit={submitDecision} class="flex flex-col gap-4">
        <Dialog.Header>
          <Dialog.Title>{deciding === "approve" ? "Approve the runner’s changes?" : "Decline the runner’s changes?"}</Dialog.Title>
          <Dialog.Description>{deciding === "approve" ? `A note with the pull request goes on the ${session.term("work").toLowerCase()}, and it moves to in review.` : "The runner closes the pull request and deletes the branch. A note says so."}</Dialog.Description>
        </Dialog.Header>
        <Field.Field>
          <Field.Label for="decision-note">Note <span class="text-muted-foreground font-normal">(optional)</span></Field.Label>
          <Textarea id="decision-note" bind:value={note} rows={3} placeholder={deciding === "approve" ? "Anything to do next" : "Why"} />
        </Field.Field>
        <Dialog.Footer>
          <Button type="button" variant="outline" onclick={() => (deciding = null)}>Back</Button>
          <Button type="submit" variant={deciding === "decline" ? "destructive" : "default"}>{deciding === "approve" ? "Approve" : "Decline"}</Button>
        </Dialog.Footer>
      </form>
    </Dialog.Content>
  </Dialog.Root>
{/if}
