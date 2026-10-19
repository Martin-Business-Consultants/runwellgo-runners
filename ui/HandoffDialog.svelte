<script>
  // Hand this to a runner: which repository (the engagement's or client's
  // default to start), what to do (the work's title and description to
  // start), and which runner, or any. Opened from a piece of work's Code tab.
  import * as Dialog from "$lib/components/ui/dialog/index.js";
  import * as Field from "$lib/components/ui/field/index.js";
  import * as Select from "$lib/components/ui/select/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Textarea } from "$lib/components/ui/textarea/index.js";
  import { get, post } from "$lib/api.js";
  import { toast, toastError } from "$lib/toast.svelte.js";
  import { focusInvalid } from "$lib/forms.js";
  import { go } from "$lib/router.svelte.js";
  import RepoPicker from "./RepoPicker.svelte";
  import { rx } from "./runners.js";

  let { open = $bindable(false), record, onqueued = undefined } = $props();
  let form = $state(null), repos = $state([]), runners = $state([]), text = $state("");
  let errors = $state({}), busy = $state(false);

  $effect(() => { if (open) start(); });
  async function start() {
    form = null; errors = {};
    try {
      const res = await get(rx("/handoff", { record }));
      form = { ...res.handoff, runner_id: res.handoff.runner_id || "any" };
      repos = res.repos.map((r) => r.value);
      runners = res.runners.map((r) => ({ value: r.value || "any", label: r.label }));
      text = res.text;
    } catch (e) { toastError(e); open = false; }
  }
  async function submit(e) {
    e.preventDefault();
    busy = true; errors = {};
    try {
      const res = await post(rx("/jobs"), { job: { record, repo: form.repo, instructions: form.instructions, runner_id: form.runner_id === "any" ? null : Number(form.runner_id) } });
      toast(res.summary);
      open = false;
      onqueued?.(res.job);
      go(res.job.path);
    } catch (err) { errors = err.errors || {}; if (!Object.keys(errors).length) toastError(err); else focusInvalid(); }
    busy = false;
  }
  const runnerLabel = $derived(runners.find((r) => r.value === form?.runner_id)?.label || "Any runner");
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="grid-cols-1 sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>Hand this to a runner</Dialog.Title>
      <Dialog.Description>{text || "A runner works on it in a fresh checkout and comes back with a pull request for you to approve."}</Dialog.Description>
    </Dialog.Header>
    {#if form}
      <form onsubmit={submit} class="flex min-w-0 flex-col gap-4">
        <Field.Field data-invalid={errors.repo ? true : undefined}>
          <Field.Label for="handoff-repo">Repository</Field.Label>
          <RepoPicker id="handoff-repo" bind:value={form.repo} options={repos} invalid={!!errors.repo} />
          {#if errors.repo}<Field.Error>{errors.repo[0]}</Field.Error>{:else}<Field.Description>owner/name on GitHub. The runner must allow it too.</Field.Description>{/if}
        </Field.Field>
        <Field.Field data-invalid={errors.instructions ? true : undefined}>
          <Field.Label for="handoff-instructions">Instructions</Field.Label>
          <Textarea id="handoff-instructions" bind:value={form.instructions} rows={6} aria-invalid={!!errors.instructions} />
          {#if errors.instructions}<Field.Error>{errors.instructions[0]}</Field.Error>{:else}<Field.Description>What to do, in plain words. The work’s brief goes with it.</Field.Description>{/if}
        </Field.Field>
        <Field.Field data-invalid={errors.runner_id ? true : undefined}>
          <Field.Label for="handoff-runner">Runner</Field.Label>
          <Select.Root type="single" bind:value={form.runner_id}>
            <Select.Trigger id="handoff-runner" class="w-full">{runnerLabel}</Select.Trigger>
            <Select.Content>
              {#each runners as r (r.value)}<Select.Item value={r.value} label={r.label}>{r.label}</Select.Item>{/each}
            </Select.Content>
          </Select.Root>
          {#if errors.runner_id}<Field.Error>{errors.runner_id[0]}</Field.Error>{/if}
        </Field.Field>
        <Dialog.Footer>
          <Button type="button" variant="outline" onclick={() => (open = false)}>Not now</Button>
          <Button type="submit" disabled={busy}>Hand it over</Button>
        </Dialog.Footer>
      </form>
    {:else}
      <div class="text-muted-foreground py-6 text-sm">Getting the form ready…</div>
    {/if}
  </Dialog.Content>
</Dialog.Root>
