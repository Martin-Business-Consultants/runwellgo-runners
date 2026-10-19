<script>
  // Settings › Runners: the machines work can be handed to. Adding one
  // gives its token once, with the commands to install and run it there,
  // and waits (live) for it to connect. Each runner shows whether it's
  // online, the tools it has, the repositories it allows, and can be
  // paused, held to fewer repositories, or removed. Below, the default
  // repository for each client or engagement, which a hand-off starts from.
  import SettingsLayout from "$lib/layout/SettingsLayout.svelte";
  import * as Card from "$lib/components/ui/card/index.js";
  import * as Dialog from "$lib/components/ui/dialog/index.js";
  import * as Field from "$lib/components/ui/field/index.js";
  import * as Table from "$lib/components/ui/table/index.js";
  import * as DropdownMenu from "$lib/components/ui/dropdown-menu/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Badge } from "$lib/components/ui/badge/index.js";
  import { Input } from "$lib/components/ui/input/index.js";
  import { Textarea } from "$lib/components/ui/textarea/index.js";
  import { Skeleton } from "$lib/components/ui/skeleton/index.js";
  import { Spinner } from "$lib/components/ui/spinner/index.js";
  import Plus from "@lucide/svelte/icons/plus";
  import Server from "@lucide/svelte/icons/server";
  import CircleCheck from "@lucide/svelte/icons/circle-check";
  import EllipsisVertical from "@lucide/svelte/icons/ellipsis-vertical";
  import Trash from "@lucide/svelte/icons/trash-2";
  import SecretBox from "$lib/components/SecretBox.svelte";
  import Combobox from "$lib/components/Combobox.svelte";
  import Time from "$lib/components/Time.svelte";
  import { onDestroy } from "svelte";
  import { get, post, patch, put, del } from "$lib/api.js";
  import { subscribe } from "$lib/live.svelte.js";
  import { session } from "$lib/session.svelte.js";
  import { toast, toastError } from "$lib/toast.svelte.js";
  import { confirm } from "$lib/confirm.svelte.js";
  import { focusInvalid } from "$lib/forms.js";
  import RepoPicker from "./RepoPicker.svelte";
  import { rx, runnerLabel, runnerClass } from "./runners.js";

  let data = $state(null), repos = $state(null);
  async function load() {
    try {
      data = await get(rx("/runners"));
      repos = await get(rx("/repos"));
    } catch (e) { toastError(e); }
  }
  load();

  // ---- adding one ----
  let adding = $state(false), name = $state(""), limit = $state(""), errors = $state({}), busy = $state(false);
  let added = $state(null); // {runner, token, commands}
  function startAdding() { adding = true; added = null; name = ""; limit = ""; errors = {}; }
  async function add(e) {
    e.preventDefault();
    busy = true; errors = {};
    try {
      added = await post(rx("/runners"), { runner: { name, allowed_repos: limit } });
      load();
    } catch (err) { errors = err.errors || {}; if (!Object.keys(errors).length) toastError(err); else focusInvalid(); }
    busy = false;
  }
  // The new runner connecting moves the dialog on.
  async function refreshAdded() {
    if (!added) return;
    try { added.runner = (await get(rx(`/runners/${added.runner.id}`), { quiet: true })).runner; } catch {}
  }
  let timer;
  const stop = subscribe("runners", (topic) => {
    if (topic !== "runner" && topic !== "resync" && topic !== "job") return;
    clearTimeout(timer);
    timer = setTimeout(() => { load(); refreshAdded(); }, 300);
  });
  // Online and offline are time: read again now and then.
  const tick = setInterval(() => { load(); refreshAdded(); }, 30000);
  onDestroy(() => { stop(); clearTimeout(timer); clearInterval(tick); });

  // ---- each runner ----
  async function pause(r, paused) {
    try { toast((await patch(rx(`/runners/${r.id}`), { runner: { paused } })).message); load(); } catch (e) { toastError(e); }
  }
  async function remove(r) {
    if (!(await confirm({ title: `Remove ${r.name}?`, message: "Its token stops working at once, so it can't pick up any more work. A job it's on fails.", confirmLabel: "Remove", destructive: true }))) return;
    try { toast((await del(rx(`/runners/${r.id}`))).message); load(); } catch (e) { toastError(e); }
  }
  let editing = $state(null), editRepos = $state(""), editErrors = $state({});
  function editLimit(r) { editing = r; editRepos = r.allowed_repos.join("\n"); editErrors = {}; }
  async function saveLimit(e) {
    e.preventDefault();
    try {
      toast((await patch(rx(`/runners/${editing.id}`), { runner: { allowed_repos: editRepos } })).message);
      editing = null; load();
    } catch (err) { editErrors = err.errors || {}; if (!Object.keys(editErrors).length) toastError(err); }
  }

  // ---- default repositories ----
  let def = $state({ record: null, repo: "", base_branch: "" }), defErrors = $state({});
  async function saveDefault(e) {
    e.preventDefault();
    defErrors = {};
    try {
      const res = await put(rx("/repos"), { repo: def });
      repos.repos = res.repos;
      toast(res.message);
      def = { record: null, repo: "", base_branch: "" };
    } catch (err) { defErrors = err.errors || {}; if (!Object.keys(defErrors).length) toastError(err); }
  }
  async function removeDefault(r) {
    try { repos.repos = (await del(rx(`/repos/${r.id}`))).repos; } catch (e) { toastError(e); }
  }
  const tools = (r) => Object.entries(r.capabilities?.tools || {});
</script>

<SettingsLayout title="Runners" description="Machines you control that do code and other heavy work handed over from Runwell, and come back with a pull request for you to approve.">
  {#snippet actions()}<Button size="sm" onclick={startAdding}><Plus />Add a runner</Button>{/snippet}

  {#if !data}
    <Skeleton class="h-40" />
  {:else}
    {#if !data.runners.length}
      <Card.Root>
        <Card.Header>
          <Card.Title>No runners yet</Card.Title>
          <Card.Description>A runner is a small program on a machine of yours (a server, a VM, a container) with git, gh and a coding agent such as Claude Code or Codex. Runwell never runs anything itself: the runner picks work up, does it in a fresh checkout and sends back its progress and a pull request.</Card.Description>
        </Card.Header>
        <Card.Content>
          <ol class="flex list-decimal flex-col gap-1 ps-5 text-sm">
            <li>Add a runner here and copy its token.</li>
            <li>On that machine, install it and run the commands it gives you.</li>
          </ol>
        </Card.Content>
        <Card.Footer><Button size="sm" onclick={startAdding}><Plus />Add a runner</Button></Card.Footer>
      </Card.Root>
    {:else}
      <div class="grid gap-4 xl:grid-cols-2">
        {#each data.runners as r (r.id)}
          <Card.Root>
            <Card.Header>
              <Card.Title class="flex flex-wrap items-center gap-2"><Server class="text-muted-foreground size-4" />{r.name}
                <Badge variant="outline" class={runnerClass(r.state)}>{runnerLabel(r.state)}</Badge></Card.Title>
              <Card.Description>
                {#if r.last_seen_at}Last seen <Time value={r.last_seen_at} />{r.capabilities.hostname ? ` on ${r.capabilities.hostname}` : ""}{r.capabilities.os ? ` (${r.capabilities.os}/${r.capabilities.arch})` : ""}{:else}It hasn’t connected yet.{/if}
              </Card.Description>
              <Card.Action>
                <DropdownMenu.Root>
                  <DropdownMenu.Trigger>{#snippet child({ props })}<Button {...props} variant="ghost" size="icon-sm"><EllipsisVertical /><span class="sr-only">Actions for {r.name}</span></Button>{/snippet}</DropdownMenu.Trigger>
                  <DropdownMenu.Content align="end">
                    <DropdownMenu.Item onSelect={() => pause(r, !r.paused)}>{r.paused ? "Resume" : "Pause"}</DropdownMenu.Item>
                    <DropdownMenu.Item onSelect={() => editLimit(r)}>Limit repositories…</DropdownMenu.Item>
                    <DropdownMenu.Separator />
                    <DropdownMenu.Item variant="destructive" onSelect={() => remove(r)}><Trash />Remove</DropdownMenu.Item>
                  </DropdownMenu.Content>
                </DropdownMenu.Root>
              </Card.Action>
            </Card.Header>
            <Card.Content class="flex flex-col gap-3 text-sm">
              {#if tools(r).length || r.capabilities.agent}
                <div class="flex flex-wrap gap-1.5" aria-label="Tools">
                  {#if r.capabilities.agent}<Badge variant="secondary" class="max-w-full"><span class="truncate">agent: {r.capabilities.agent.split("/").pop()}</span></Badge>{/if}
                  {#each tools(r) as [t, v] (t)}<Badge variant="outline" class="font-normal"><span class="font-medium">{t}</span> <span class="text-muted-foreground tabular-nums">{v}</span></Badge>{/each}
                </div>
              {/if}
              <div>
                <p class="text-muted-foreground text-xs">Repositories it allows</p>
                {#if r.runner_repos.length}
                  <p class="break-all">{r.runner_repos.join(", ")}</p>
                {:else}
                  <p class="text-muted-foreground">It says once it connects.</p>
                {/if}
                {#if r.allowed_repos.length}<p class="text-muted-foreground mt-1 text-xs break-all">Runwell holds it to: {r.allowed_repos.join(", ")}</p>{/if}
              </div>
              {#if r.active_jobs}<p><a class="text-primary hover:underline" href={`/runners?runner=${r.id}&status=active`}>{r.active_jobs} {r.active_jobs === 1 ? "job" : "jobs"} running</a></p>{/if}
            </Card.Content>
          </Card.Root>
        {/each}
      </div>
    {/if}

    <Card.Root>
      <Card.Header>
        <Card.Title>Default repositories</Card.Title>
        <Card.Description>Where a client’s or an engagement’s work goes, so handing it over starts with the right repository. An engagement’s wins over its client’s.</Card.Description>
      </Card.Header>
      <Card.Content class="flex flex-col gap-4">
        {#if repos?.repos.length}
          <Table.Root>
            <Table.Header><Table.Row><Table.Head>For</Table.Head><Table.Head>Repository</Table.Head><Table.Head class="hidden sm:table-cell">Base branch</Table.Head><Table.Head class="w-10"><span class="sr-only">Remove</span></Table.Head></Table.Row></Table.Header>
            <Table.Body>
              {#each repos.repos as r (r.id)}
                <Table.Row>
                  <Table.Cell class="whitespace-normal">{r.record_name}<span class="text-muted-foreground block text-xs">{r.record.split(":")[0]}</span></Table.Cell>
                  <Table.Cell class="max-w-64 font-mono text-xs break-all whitespace-normal">{r.repo}</Table.Cell>
                  <Table.Cell class="hidden sm:table-cell">{r.base_branch || "Its default"}</Table.Cell>
                  <Table.Cell><Button variant="ghost" size="icon-sm" onclick={() => removeDefault(r)}><Trash /><span class="sr-only">Remove the default for {r.record_name}</span></Button></Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
        {/if}
        {#if repos}
          <form onsubmit={saveDefault} class="grid gap-3 sm:grid-cols-[1fr_1fr_8rem_auto] sm:items-start">
            <Field.Field data-invalid={defErrors.record ? true : undefined}>
              <Field.Label for="def-record" class="sr-only">Client or engagement</Field.Label>
              <Combobox id="def-record" bind:value={def.record} options={repos.records} placeholder="A client or engagement" invalid={!!defErrors.record} />
              {#if defErrors.record}<Field.Error>{defErrors.record[0]}</Field.Error>{/if}
            </Field.Field>
            <Field.Field data-invalid={defErrors.repo ? true : undefined}>
              <Field.Label for="def-repo" class="sr-only">Repository</Field.Label>
              <RepoPicker id="def-repo" bind:value={def.repo} options={repos.known} invalid={!!defErrors.repo} />
              {#if defErrors.repo}<Field.Error>{defErrors.repo[0]}</Field.Error>{/if}
            </Field.Field>
            <Field.Field data-invalid={defErrors.base_branch ? true : undefined}>
              <Field.Label for="def-base" class="sr-only">Base branch</Field.Label>
              <Input id="def-base" bind:value={def.base_branch} placeholder="main" />
              {#if defErrors.base_branch}<Field.Error>{defErrors.base_branch[0]}</Field.Error>{/if}
            </Field.Field>
            <Button type="submit" variant="outline">Set default</Button>
          </form>
        {/if}
      </Card.Content>
    </Card.Root>
  {/if}
</SettingsLayout>

<Dialog.Root bind:open={adding}>
  <Dialog.Content class="grid-cols-1 sm:max-w-xl">
    {#if !added}
      <form onsubmit={add} class="flex flex-col gap-4">
        <Dialog.Header>
          <Dialog.Title>Add a runner</Dialog.Title>
          <Dialog.Description>Name the machine it runs on. You get a token for it next, shown once.</Dialog.Description>
        </Dialog.Header>
        <Field.Field data-invalid={errors.name ? true : undefined}>
          <Field.Label for="runner-name">Name</Field.Label>
          <Input id="runner-name" bind:value={name} placeholder="Build box" aria-invalid={!!errors.name} />
          {#if errors.name}<Field.Error>{errors.name[0]}</Field.Error>{/if}
        </Field.Field>
        <Field.Field data-invalid={errors.allowed_repos ? true : undefined}>
          <Field.Label for="runner-repos">Repositories it may work on</Field.Label>
          <Input id="runner-repos" bind:value={limit} placeholder="acme/website, acme/*" aria-invalid={!!errors.allowed_repos} />
          {#if errors.allowed_repos}<Field.Error>{errors.allowed_repos[0]}</Field.Error>{:else}<Field.Description>Optional. The runner keeps its own list too: a job goes to it only when both allow the repository.</Field.Description>{/if}
        </Field.Field>
        <Dialog.Footer>
          <Button type="button" variant="outline" onclick={() => (adding = false)}>Not now</Button>
          <Button type="submit" disabled={busy}>Add runner</Button>
        </Dialog.Footer>
      </form>
    {:else}
      <Dialog.Header>
        <Dialog.Title>Set up {added.runner.name}</Dialog.Title>
        <Dialog.Description>On the machine, in a container or VM of its own. It needs git, gh (signed in) and your coding agent’s CLI.</Dialog.Description>
      </Dialog.Header>
      <div class="flex min-w-0 flex-col gap-4">
        <SecretBox value={added.token} note="Its token. Copy it now: it isn’t shown again." />
        <div class="flex min-w-0 flex-col gap-1">
          <p class="text-sm font-medium">1. Install it</p>
          <SecretBox value={added.commands.install} />
        </div>
        <div class="flex min-w-0 flex-col gap-1">
          <p class="text-sm font-medium">2. Set it up, then run it</p>
          <SecretBox value={added.commands.init} />
          <p class="text-muted-foreground text-xs">Put the repositories it may touch after <code>--repos</code>. Check it with <code>runwell-runner doctor</code>.</p>
          <SecretBox value={added.commands.run} />
        </div>
        <div class="flex items-center gap-2 rounded-md border p-3 text-sm" role="status">
          {#if added.runner.state === "waiting"}
            <Spinner class="text-muted-foreground" />Waiting for it to connect…
          {:else}
            <CircleCheck class="size-4 text-emerald-600 dark:text-emerald-400" />Connected{#if added.runner.capabilities.hostname}: {added.runner.capabilities.hostname}{/if}. Hand it some {session.term("work").toLowerCase()} from its Code tab.
          {/if}
        </div>
      </div>
      <Dialog.Footer><Button onclick={() => (adding = false)}>Done</Button></Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>

<Dialog.Root open={!!editing} onOpenChange={(v) => { if (!v) editing = null; }}>
  <Dialog.Content>
    {#if editing}
      <form onsubmit={saveLimit} class="flex flex-col gap-4">
        <Dialog.Header>
          <Dialog.Title>Limit {editing.name}’s repositories</Dialog.Title>
          <Dialog.Description>Runwell hands it work only for these, one per line (owner/name, or owner/* for all of an owner’s). Empty: whatever the runner itself allows.</Dialog.Description>
        </Dialog.Header>
        <Field.Field data-invalid={editErrors.allowed_repos ? true : undefined}>
          <Field.Label for="limit-repos">Repositories</Field.Label>
          <Textarea id="limit-repos" bind:value={editRepos} rows={4} class="font-mono text-xs" />
          {#if editErrors.allowed_repos}<Field.Error>{editErrors.allowed_repos[0]}</Field.Error>{/if}
        </Field.Field>
        <Dialog.Footer>
          <Button type="button" variant="outline" onclick={() => (editing = null)}>Back</Button>
          <Button type="submit">Save</Button>
        </Dialog.Footer>
      </form>
    {/if}
  </Dialog.Content>
</Dialog.Root>
