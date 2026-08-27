import { execFileSync } from "node:child_process";

// The admin UI's quota, reset and key actions only render for accounts
// that have completed the OAuth flow, i.e. that have a `users` row. The
// suite forges session cookies rather than driving GitHub, so no such
// row exists unless we make one.
//
// Seeding goes through the sqlite3 CLI against the server's live
// database. The server has already run its migrations by the time
// Playwright's webServer health check passes, so the schema is in place
// and this only inserts rows.

const STATE_DIR = process.env.E2E_STATE_DIR ?? "/tmp/typst-d2-mcp-e2e";
const DB = `${STATE_DIR}/auth.sqlite`;

function sql(statement: string): string {
  // `.timeout` matters. The server under test writes to this same file
  // while the suite reads it, and the sqlite3 CLI defaults to a zero
  // busy timeout — so a plain SELECT fails outright with
  // "database is locked (5)" the moment a write is in flight. That is
  // not contention worth failing a test over; it is contention worth
  // waiting out.
  return execFileSync("sqlite3", ["-cmd", ".timeout 5000", DB, statement], {
    encoding: "utf8",
  });
}

/** Insert a user who has "signed in", returning their identity key. */
export function seedSignedInUser(githubId: number, login: string): string {
  sql(
    `INSERT OR REPLACE INTO users(github_id, github_login, email)
     VALUES(${githubId}, '${login}', '${login}@example.test')`,
  );
  return `gh:${githubId}`;
}

/**
 * Give a user an API key so the revoke-keys action has something to do.
 *
 * last_used_at is set, not left NULL, because a *used* key is what the
 * production database actually contains — and rendering one used to
 * fail: the user-list query wrapped that column in MAX(), which returns
 * SQLite text rather than a timestamp. Seeding an unused key renders a
 * NULL, which scans fine and hides the bug.
 */
export function seedAPIKey(login: string): void {
  sql(
    `INSERT INTO api_keys(user_id, key_hash, last_used_at)
     SELECT id, randomblob(32), CURRENT_TIMESTAMP
       FROM users WHERE github_login = '${login}'`,
  );
}

export function countInvites(login: string): number {
  return Number(
    sql(`SELECT COUNT(*) FROM invites WHERE github_login = '${login}'`).trim(),
  );
}

export function countUsers(login: string): number {
  return Number(
    sql(`SELECT COUNT(*) FROM users WHERE github_login = '${login}'`).trim(),
  );
}

export function quotaFor(login: string): string {
  return sql(
    `SELECT IFNULL(CAST(quota_per_day AS TEXT), 'NULL')
       FROM users WHERE github_login = '${login}'`,
  ).trim();
}

/**
 * A user's workspace storage budget override in bytes, or 'NULL' when the
 * workspace inherits the deployment default. Read from workspace_budgets,
 * which is keyed by the tenant/workspace id ('gh:'||github_id).
 */
export function budgetFor(login: string): string {
  return sql(
    `SELECT IFNULL(CAST(b.budget_bytes AS TEXT), 'NULL')
       FROM users u
       LEFT JOIN workspace_budgets b ON b.user_id = 'gh:' || u.github_id
      WHERE u.github_login = '${login}'`,
  ).trim();
}

export function auditActions(): string[] {
  return sql(`SELECT action FROM admin_audit ORDER BY id`)
    .split("\n")
    .filter(Boolean);
}

/** Set a user's quota override directly: NULL inherits, 0 unlimited, N caps. */
export function seedQuota(login: string, quota: number | null): void {
  sql(
    `UPDATE users SET quota_per_day = ${quota === null ? "NULL" : quota}
      WHERE github_login = '${login}'`,
  );
}

/**
 * Number of shared namespaces — what the admin UI calls organisations.
 *
 * Personal namespaces are excluded the same way ListOrgs excludes them:
 * everyone gets one at first sign-in, so counting them here would make
 * this number depend on how many users happen to have signed in.
 */
export function orgCount(): number {
  return Number(
    sql(
      `SELECT COUNT(*) FROM namespace_names
        WHERE is_primary = 1 AND name NOT LIKE 'gh-%'`,
    ).trim(),
  );
}

/**
 * Number of members in a namespace, addressed by NAME. Membership hangs
 * off the namespace id, so this joins through the name — which is the
 * whole point of the indirection: a rename does not change the answer.
 */
export function orgMemberCount(slug: string): number {
  return Number(
    sql(
      `SELECT COUNT(*) FROM namespace_members m
         JOIN namespace_names nn ON nn.namespace_id = m.namespace_id
        WHERE nn.name = '${slug}'`,
    ).trim(),
  );
}

/**
 * A member's role in a namespace, addressed by NAME, or '' when they are
 * not a member. Ownership is what grants publishing, so this is the
 * column that decides whether an organisation is usable at all.
 */
export function orgMemberRole(slug: string, login: string): string {
  return sql(
    `SELECT m.role
       FROM namespace_members m
       JOIN namespace_names nn ON nn.namespace_id = m.namespace_id
       JOIN users u ON 'gh:' || u.github_id = m.user_id
      WHERE nn.name = '${slug}' AND u.github_login = '${login}'`,
  ).trim();
}
