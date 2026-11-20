const KNOWN: Record<string, string> = {
  claude: "amber",
  codex: "ink",
  opencode: "sky",
  github: "ink",
  gitlab: "amber",
  bitbucket: "sky",
  jira: "sky",
  atlassian: "sky",
  slack: "violet",
  linear: "violet",
  notion: "ink",
};
const CYCLE = ["mint", "violet", "sky", "amber", "rose"];

// Avatar colour for an agent, provider or server name: fixed for known brands, stable per name otherwise.
export function toneOf(name: string): string {
  const key = name.toLowerCase().replace(/[^a-z]/g, "");
  for (const [k, t] of Object.entries(KNOWN)) if (key.startsWith(k)) return t;
  let h = 0;
  for (const c of key) h = (h * 31 + c.charCodeAt(0)) | 0;
  return CYCLE[Math.abs(h) % CYCLE.length];
}
