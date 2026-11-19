// Subsequence match: consecutive, basename and word-start hits score higher; null when the query is not a subsequence.
export function fuzzyScore(query: string, path: string): number | null {
  const q = query.toLowerCase(),
    p = path.toLowerCase();
  if (!q) return 0;
  const base = p.lastIndexOf("/") + 1;
  let qi = 0,
    score = 0,
    last = -2;
  for (let i = 0; i < p.length && qi < q.length; i++) {
    if (p[i] !== q[qi]) continue;
    score += 1;
    if (i === last + 1) score += 3;
    if (i >= base) score += 2;
    if (i === 0 || "/._-".includes(p[i - 1])) score += 3;
    last = i;
    qi++;
  }
  return qi === q.length ? score - path.length / 100 : null;
}

export function rankPaths(query: string, all: string[], limit = 50): string[] {
  const out: { path: string; score: number }[] = [];
  for (const path of all) {
    const score = fuzzyScore(query, path);
    if (score !== null) out.push({ path, score });
  }
  return out
    .sort((a, b) => b.score - a.score)
    .slice(0, limit)
    .map((x) => x.path);
}
