// Runners (plugins/runners): hand code to a machine you control. The jobs
// at /runners and each job's live log, Settings › Runners, and a Code tab on
// each piece of work. Shown only while the plugin is on.
import { addTab } from "$lib/record-tabs.js";
import { addSettingsPage } from "$lib/settings-nav.js";
import { isOn } from "../../routes/plugins/extensions.svelte.js";
import JobsPage from "./JobsPage.svelte";
import JobPage from "./JobPage.svelte";
import RunnersSettings from "./RunnersSettings.svelte";
import WorkCode from "./WorkCode.svelte";

export const key = "runners";
export const routes = [
  ["/runners", JobsPage],
  ["/runners/jobs/:id", JobPage],
  ["/settings/runners", RunnersSettings],
];

export function setup() {
  addTab("Todo", { key, label: () => "Code", component: WorkCode, order: 95, when: () => isOn(key) });
  addSettingsPage({ href: "/settings/runners", label: "Runners", group: "Plugins and AI", perm: "runners:manage", order: 46, when: () => isOn(key) });
}
