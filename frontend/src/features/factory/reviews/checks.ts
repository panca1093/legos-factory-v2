import type { QualityCheck } from "../../../types/api";

const BASELINE_PREFIX = "qc_base_";

export const isBaseline = (c: QualityCheck) => c.id.startsWith(BASELINE_PREFIX);

export const realChecks = (checks: QualityCheck[]) =>
  checks.filter((c) => !isBaseline(c));

export function baselineFailures(checks: QualityCheck[]): Set<string> {
  return new Set(
    checks
      .filter((c) => isBaseline(c) && c.outcome !== "PASS")
      .map((c) => c.name),
  );
}
