<script>
  // Runners › Jobs: every piece of work handed to a runner, newest first,
  // narrowed by status, runner and who asked; each row's state updates as
  // runners report.
  import * as Card from "$lib/components/ui/card/index.js";
  import * as Table from "$lib/components/ui/table/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Badge } from "$lib/components/ui/badge/index.js";
  import { Skeleton } from "$lib/components/ui/skeleton/index.js";
  import GitPullRequest from "@lucide/svelte/icons/git-pull-request";
  import Settings from "@lucide/svelte/icons/settings";
  import IndexToolbar from "$lib/components/IndexToolbar.svelte";
  import Time from "$lib/components/Time.svelte";
  import { onDestroy } from "svelte";
  import { get } from "$lib/api.js";
  import { page } from "$lib/page.svelte.js";
  import { session } from "$lib/session.svelte.js";
  import { router, onQuery } from "$lib/router.svelte.js";
  import { subscribe } from "$lib/live.svelte.js";
  import { nav } from "$lib/nav.svelte.js";
  import { toastError } from "$lib/toast.svelte.js";
  import { rx, jobClass, repoName, STATUS_OPTIONS } from "./runners.js";

  page.set("Runners");
  let data = $state(null);
  const status = $derived(router.query.get("status") || "all");
  const runner = $derived(router.query.get("runner") || "all");
  const person = $derived(router.query.get("person") || "all");

  async function load() {
    try {
      data = await get(rx("/jobs", { status: router.query.get("status"), runner: router.query.get("runner"), person: router.query.get("person") }));
    } catch (e) { toastError(e); }
  }
  onQuery(load);

  // A job moved somewhere: read the list again (a beat later, once for a
  // burst), and the sidebar's count.
  let timer;
  const stop = subscribe("runners", (topic) => {
    if (topic !== "job" && topic !== "resync") return;
    clearTimeout(timer);
    timer = setTimeout(() => { load(); nav.refresh(); }, 400);
  });
  onDestroy(() => { stop(); clearTimeout(timer); });

  const filtered = $derived(status !== "all" || runner !== "all" || person !== "all");
</script>

<IndexToolbar filters={data ? [
  { param: "status", current: status, title: "Status", options: STATUS_OPTIONS },
  { param: "runner", current: runner, title: "Runner", options: data.runners },
  { param: "person", current: person, title: "Asked by", options: data.people },
] : []}>
  {#if data?.can_manage}<Button href="/settings/runners" variant="outline" size="sm"><Settings />Runners</Button>{/if}
</IndexToolbar>

{#if !data}
  <Skeleton class="h-48" />
{:else if !data.has_runners && !data.jobs.length}
  <Card.Root>
    <Card.Header>
      <Card.Title>Hand heavy work to a runner</Card.Title>
      <Card.Description>A runner is a machine you control, with git, gh and a coding agent such as Claude Code. Hand it a piece of {session.term("work").toLowerCase()}: it works in a fresh checkout, streams its progress here and comes back with a pull request for you to approve.</Card.Description>
    </Card.Header>
    <Card.Content>
      <ol class="flex list-decimal flex-col gap-2 ps-5 text-sm">
        <li>{#if data.can_manage}<a class="text-primary hover:underline" href="/settings/runners">Add a runner</a>{:else}Ask someone who manages runners to add one{/if} and run the two commands it gives you on that machine.</li>
        <li>Open a piece of {session.term("work").toLowerCase()} and choose <strong>Hand this to a runner</strong> on its Code tab.</li>
      </ol>
    </Card.Content>
  </Card.Root>
{:else}
  <Card.Root class="py-0">
    <Table.Root>
      <Table.Header>
        <Table.Row>
          <Table.Head>{session.term("work")}</Table.Head>
          <Table.Head>Status</Table.Head>
          <Table.Head class="hidden md:table-cell">Runner</Table.Head>
          <Table.Head class="hidden md:table-cell">Asked by</Table.Head>
          <Table.Head class="hidden sm:table-cell">Asked</Table.Head>
          <Table.Head class="w-10"><span class="sr-only">Pull request</span></Table.Head>
        </Table.Row>
      </Table.Header>
      <Table.Body>
        {#each data.jobs as j (j.id)}
          <Table.Row>
            <Table.Cell class="max-w-80 whitespace-normal">
              <a href={j.path} class="block hover:underline">
                <span class="font-medium">{j.record_title || j.record}</span>
                <span class="text-muted-foreground block truncate text-xs">Job {j.id} · {repoName(j.repo)}</span>
              </a>
            </Table.Cell>
            <Table.Cell><Badge variant="outline" class={jobClass(j.status)}>{j.status_label}</Badge></Table.Cell>
            <Table.Cell class="hidden md:table-cell">{j.runner?.name || "—"}</Table.Cell>
            <Table.Cell class="hidden md:table-cell">{j.requested_by.name}{#if j.via_agent}<span class="text-muted-foreground"> via {j.via_agent}</span>{/if}</Table.Cell>
            <Table.Cell class="hidden sm:table-cell"><Time value={j.created_at} /></Table.Cell>
            <Table.Cell>
              {#if j.pr_url}<Button href={j.pr_url} target="_blank" rel="noopener" variant="ghost" size="icon-sm" title="Open the pull request"><GitPullRequest /><span class="sr-only">Pull request for job {j.id}</span></Button>{/if}
            </Table.Cell>
          </Table.Row>
        {:else}
          <Table.Row>
            <Table.Cell colspan={6} class="text-muted-foreground text-sm whitespace-normal">
              {#if filtered}
                No jobs match. <a class="text-foreground underline-offset-4 hover:underline" href="/runners">Show all</a>
              {:else}
                No jobs yet. Open a piece of {session.term("work").toLowerCase()} and hand it to a runner from its Code tab.
              {/if}
            </Table.Cell>
          </Table.Row>
        {/each}
      </Table.Body>
    </Table.Root>
  </Card.Root>
{/if}
