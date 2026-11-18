import type { QualityCheck } from "../../../types/api";

const BASELINE_PREFIX = "qc_base_";
const GO_TEST_FAILURE = /^\s*--- FAIL: (\S+)/gm;

export const isBaseline = (c: QualityCheck) => c.id.startsWith(BASELINE_PREFIX);

export const realChecks = (checks: QualityCheck[]) =>
  checks.filter((c) => !isBaseline(c));

const failingTests = (output: string) =>
  [...output.matchAll(GO_TEST_FAILURE)].map((m) => m[1]);

export function failsOnMain(checks: QualityCheck[], c: QualityCheck): boolean {
  if (c.optional || c.outcome === "PASS") return false;
  const b = checks.find((x) => isBaseline(x) && x.name === c.name);
  if (!b || b.command !== c.command || b.outcome === "PASS") return false;
  const before = new Set(failingTests(b.output));
  return failingTests(c.output).every((t) => before.has(t));
}
