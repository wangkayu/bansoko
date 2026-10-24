import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";
import path from "node:path";
import { expect } from "@playwright/test";

const execFile = promisify(execFileCallback);
export async function readSessionFixtureIdentity(): Promise<{ title: string; stable_item_key: string }> {
  const stateDir = process.env.BANSOKO_BROWSER_SESSION_STATE_DIR?.trim();
  if (!stateDir) throw new Error("local session metadata is required");
  const { readFile } = await import("node:fs/promises");
  const session = JSON.parse(await readFile(path.join(stateDir, "browser-session.json"), "utf8")) as { fixture?: { title?: string; stable_item_key?: string } };
  const fixture = session.fixture;
  if (!fixture?.title || !fixture.stable_item_key) throw new Error("session fixture raw title/stable item key missing");
  return { title: fixture.title, stable_item_key: fixture.stable_item_key };
}

export type SnapshotTarget = { managed_id: string; source_id: string; release_id: string };
type OwnerRow = { type: string; id: string; state: string | null; version: number; updated_at: string; fence_epoch: number | null };
type WorkRow = { id: string; owner_type: string; owner_id: string; kind: string; state: string; fencing_token: number; attempt: number; updated_at: string; managed_anime_id: string | null; owner_lifecycle_epoch: number | null; external_effect_admitted_at: string | null };
type CandidateRow = { id: string; version: number; updated_at: string; status: string; activated_managed_anime_id: string | null; current_revision: number | null; current_revision_digest: string | null; draft_digest: string };
type RevisionRow = { candidate_id: string; revision: number; draft_digest: string; effective_digest: string; parser_policy_digest: string; created_at: string };
export type DisposableSnapshot = {
  database: string;
  read_only: "on";
  isolation: "repeatable read";
  owners: OwnerRow[];
  work_items: WorkRow[];
  candidates: CandidateRow[];
  candidate_revisions: RevisionRow[];
  target: SnapshotTarget | null;
  scoped_owners: OwnerRow[] | null;
  scoped_work_items: WorkRow[] | null;
};

// Run Playwright on the session host with Linux-local dependencies. This has no
// remote-shell/HTTP fallback: absence of local active session evidence is red.
// Neither DSN nor credentials enter argv, stdout, assertion messages or artifacts.
export async function readDisposableSnapshot(target?: SnapshotTarget): Promise<DisposableSnapshot> {
  const stateDir = process.env.BANSOKO_BROWSER_SESSION_STATE_DIR?.trim();
  if (!stateDir) throw new Error("local BANSOKO_BROWSER_SESSION_STATE_DIR is required for DB invariance evidence");
  const helper = path.resolve(process.cwd(), "../scripts/ci/real-effect-browser-db-snapshot.py");
  const args = [helper, "--state-dir", stateDir];
  if (target) for (const [key, value] of Object.entries(target)) args.push(`--${key.replaceAll("_", "-")}`, value);
  let stdout: string;
  try {
    ({ stdout } = await execFile("python3", args, { timeout: 30000, maxBuffer: 8 * 1024 * 1024 }));
  } catch (error) {
    // Only the helper's closed failure code is surfaced; raw stderr stays suppressed.
    const stderr = String((error as { stderr?: unknown })?.stderr ?? "");
    const code = /\(([a-z_]{1,64})\)\s*$/.exec(stderr.trim())?.[1] ?? "unclassified";
    throw new Error(`disposable DB snapshot guard/query failed (${code}; details suppressed)`);
  }
  return JSON.parse(stdout) as DisposableSnapshot;
}

function keyed<T>(rows: T[], key: (row: T) => string): Map<string, T> {
  const entries = rows.map((row) => [key(row), row] as const);
  const result = new Map(entries);
  expect(result.size, "snapshot primary keys must be unique").toBe(rows.length);
  return result;
}

export function assertReadOnlySnapshots(before: DisposableSnapshot, after: DisposableSnapshot, candidateAllowance?: { id: string; columns: Array<"version" | "updated_at"> }) {
  expect(after.database).toBe(before.database);
  expect(after.target).toEqual(before.target);
  expect(after.read_only).toBe("on");
  expect(after.isolation).toBe("repeatable read");
  const revisionsBefore = keyed(before.candidate_revisions, (row) => JSON.stringify([row.candidate_id, row.revision]));
  const revisionsAfter = keyed(after.candidate_revisions, (row) => JSON.stringify([row.candidate_id, row.revision]));
  expect([...revisionsAfter.keys()].sort(), "immutable candidate revision PK set").toEqual([...revisionsBefore.keys()].sort());
  for (const [id, row] of revisionsBefore) expect(revisionsAfter.get(id), `unchanged revision ${id}`).toEqual(row);
  for (const field of ["owners", "work_items", "scoped_owners", "scoped_work_items"] as const) {
    const a = before[field];
    const b = after[field];
    if (a === null || b === null) { expect(b).toEqual(a); continue; }
    const key = (row: OwnerRow | WorkRow) => "type" in row ? JSON.stringify([row.type, row.id]) : row.id;
    const left = keyed<OwnerRow | WorkRow>(a, key);
    const right = keyed<OwnerRow | WorkRow>(b, key);
    // Identity sets, not counts; then each observed state/version/update/fence.
    expect([...right.keys()].sort(), `${field} primary-key set`).toEqual([...left.keys()].sort());
    for (const [id, row] of left) expect(right.get(id), `${field} unchanged row ${id}`).toEqual(row);
  }
  const left = keyed(before.candidates, (row) => row.id);
  const right = keyed(after.candidates, (row) => row.id);
  expect([...right.keys()].sort()).toEqual([...left.keys()].sort());
  if (candidateAllowance) {
    expect(left.has(candidateAllowance.id), "allowed candidate must exist before").toBe(true);
    expect(right.has(candidateAllowance.id), "allowed candidate must exist after").toBe(true);
  }
  for (const [id, row] of left) {
    const next = right.get(id)!;
    for (const column of ["id", "version", "updated_at", "status", "activated_managed_anime_id", "current_revision", "current_revision_digest", "draft_digest"] as const) {
      if ((column === "version" || column === "updated_at") && id === candidateAllowance?.id && candidateAllowance.columns.includes(column)) continue;
      expect(next[column], `candidate ${id}.${column}`).toEqual(row[column]);
    }
  }
}
