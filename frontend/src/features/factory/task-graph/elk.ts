import type { LayoutEngine } from "./layout";

let engine: Promise<LayoutEngine> | undefined;

// elkjs is ~1 MB, so it is fetched only when the task graph first lays out,
// and runs in a Web Worker to keep layout off the main thread.
export function loadElk(): Promise<LayoutEngine> {
  engine ??= Promise.all([
    import("elkjs/lib/elk-api.js"),
    import("elkjs/lib/elk-worker.min.js?url"),
  ])
    .then(([api, worker]) => new api.default({ workerUrl: worker.default }))
    .catch((error: unknown) => {
      engine = undefined;
      throw error;
    });
  return engine;
}
