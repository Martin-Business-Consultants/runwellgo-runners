<script>
  // A repository: one of those Runwell knows (the defaults, the runners'
  // allowlists) or any owner/name typed in, as a searchable combobox.
  import * as Popover from "$lib/components/ui/popover/index.js";
  import * as Command from "$lib/components/ui/command/index.js";
  import ChevronsUpDown from "@lucide/svelte/icons/chevrons-up-down";
  import Check from "@lucide/svelte/icons/check";
  import Plus from "@lucide/svelte/icons/plus";

  let { value = $bindable(""), options = [], id = undefined, invalid = false, placeholder = "owner/name" } = $props();
  let open = $state(false), q = $state("");
  const typed = $derived(q.trim());
  const pick = (v) => { value = v; open = false; q = ""; };
</script>

<Popover.Root bind:open>
  <Popover.Trigger {id} role="combobox" aria-expanded={open} aria-invalid={invalid || undefined}
    class="border-input dark:bg-input/30 focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:border-destructive flex h-9 w-full min-w-0 items-center gap-2 rounded-md border bg-transparent px-3 text-left text-sm shadow-xs outline-none focus-visible:ring-3">
    <span class={["min-w-0 flex-1 truncate", !value && "text-muted-foreground"]}>{value || placeholder}</span>
    <ChevronsUpDown class="text-muted-foreground size-4 shrink-0" />
  </Popover.Trigger>
  <Popover.Content class="w-(--bits-popover-anchor-width) min-w-64 p-0" align="start">
    <Command.Root>
      <Command.Input bind:value={q} placeholder="Find or type owner/name…" />
      <Command.List>
        <Command.Empty>{typed ? "" : "No repositories yet: type one."}</Command.Empty>
        {#if typed && !options.includes(typed)}
          <Command.Group>
            <Command.Item value={"use:" + typed} forceMount onSelect={() => pick(typed)}><Plus />Use “{typed}”</Command.Item>
          </Command.Group>
        {/if}
        {#if options.length}
          <Command.Group heading="Known repositories">
            {#each options as o (o)}
              <Command.Item value={o} onSelect={() => pick(o)}>
                <span class="min-w-0 flex-1 truncate">{o}</span>{#if o === value}<Check class="size-4" />{/if}
              </Command.Item>
            {/each}
          </Command.Group>
        {/if}
      </Command.List>
    </Command.Root>
  </Popover.Content>
</Popover.Root>
