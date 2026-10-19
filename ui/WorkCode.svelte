<script>
  // A piece of work's Code tab: hand it to a runner, and its jobs with
  // where each stands and its pull request, updating as runners report.
  import * as Card from "$lib/components/ui/card/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Badge } from "$lib/components/ui/badge/index.js";
  import Code from "@lucide/svelte/icons/code";
  import GitPullRequest from "@lucide/svelte/icons/git-pull-request";
  import Time from "$lib/components/Time.svelte";
  import { onDestroy } from "svelte";
  import { get } from "$lib/api.js";
  import { session } from "$lib/session.svelte.js";
  import { subscribe } from "$lib/live.svelte.js";
  import HandoffDialog from "./HandoffDialog.svelte";
  import { rx, jobClass, repoName } from "./runners.js";

  let { record } = $props();
  const ref = $derived(`Todo:${record.id}`);
  let jobs = $state(null), hasRunners = $state(true), open = $state(false);

  async function load() {
    try {
      const res = await get(rx("/jobs", { record: ref }), { quiet: true });
      jobs = res.jobs; hasRunners = res.has_runners;
    } catch { jobs = []; }
  }
  load();
  const stop = subscribe("runners", (topic, data) => {
    if (topic === "resync" || (topic === "job" && data.record === ref)) load();
  });
  onDestroy(stop);
</script>

<div class="flex flex-col gap-3">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <p class="text-muted-foreground text-sm">Hand the coding to a runner: it works in a fresh checkout and comes back with a pull request for you to approve.</p>
    {#if session.can("runners:use")}<Button size="sm" onclick={() => (open = true)}><Code />Hand this to a runner</Button>{/if}
  </div>
  {#if jobs?.length}
    <Card.Root class="gap-0 py-0">
      <ul class="divide-y">
        {#each jobs as j (j.id)}
          <li class="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3">
            <a href={j.path} class="min-w-0 flex-1 hover:underline">
              <span class="block truncate text-sm font-medium">Job {j.id}: {j.instructions.split("\n")[0]}</span>
              <span class="text-muted-foreground block truncate text-xs">{repoName(j.repo)} · {j.requested_by.name} · <Time value={j.created_at} /></span>
            </a>
            <Badge variant="outline" class={jobClass(j.status)}>{j.status_label}</Badge>
            {#if j.pr_url}<Button href={j.pr_url} target="_blank" rel="noopener" variant="ghost" size="icon-sm" title="Open the pull request"><GitPullRequest /><span class="sr-only">Pull request for job {j.id}</span></Button>{/if}
          </li>
        {/each}
      </ul>
    </Card.Root>
  {:else if jobs}
    <p class="text-muted-foreground rounded-md border border-dashed p-4 text-sm">
      {#if hasRunners}
        Nothing handed over yet.
      {:else}
        There’s no runner yet. {#if session.can("runners:manage")}<a class="text-primary hover:underline" href="/settings/runners">Add one</a> (a machine of yours with git, gh and a coding agent), then hand this over.{:else}Ask someone who manages runners to add one.{/if}
      {/if}
    </p>
  {/if}
</div>

<HandoffDialog bind:open record={ref} />
