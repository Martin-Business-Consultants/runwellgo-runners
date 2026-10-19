<script>
  // A job's log: monospace lines as the runner sends them (the runner's own
  // steps quieter, stderr tinted), following new lines while you're at the
  // bottom and staying put once you scroll up to read.
  import { tick } from "svelte";
  import ArrowDown from "@lucide/svelte/icons/arrow-down";
  import { Button } from "$lib/components/ui/button/index.js";

  let { events = [], live = false } = $props();
  let box = $state(null);
  let pinned = $state(true);

  function onscroll() {
    if (!box) return;
    pinned = box.scrollHeight - box.scrollTop - box.clientHeight < 24;
  }
  async function bottom() {
    await tick();
    if (box) box.scrollTop = box.scrollHeight;
  }
  // New lines: follow them only when already at the bottom.
  $effect(() => {
    events.length;
    if (pinned) bottom();
  });
  const time = (iso) => (iso ? new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }) : "");
</script>

<div class="relative">
  <!-- A scrolling log is reached by keyboard to scroll it. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div bind:this={box} {onscroll} role="log" aria-live={live ? "polite" : "off"} aria-label="Job log" tabindex="0"
    class="bg-muted/40 focus-visible:ring-ring/50 max-h-[28rem] min-h-24 overflow-auto rounded-md border p-3 font-mono text-xs leading-5 outline-none focus-visible:ring-3">
    {#each events as e (e.seq)}
      <div class="flex gap-3">
        <span class="text-muted-foreground hidden shrink-0 tabular-nums select-none sm:inline">{time(e.at)}</span>
        <span class={["min-w-0 break-words whitespace-pre-wrap",
          e.stream === "runner" && "text-primary font-medium",
          e.stream === "stderr" && "text-amber-800 dark:text-amber-300"]}>{e.text || " "}</span>
      </div>
    {:else}
      <p class="text-muted-foreground font-sans text-sm">{live ? "Waiting for the runner to start…" : "Nothing was logged."}</p>
    {/each}
  </div>
  {#if !pinned}
    <Button size="sm" variant="secondary" class="absolute right-3 bottom-3 shadow-sm" onclick={() => { pinned = true; bottom(); }}><ArrowDown />Latest</Button>
  {/if}
</div>
