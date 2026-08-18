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
  return execFileSync("sqlite3", [DB, statement], { encoding: "utf8" });
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

export function auditActions(): string[] {
  return sql(`SELECT action FROM admin_audit ORDER BY id`)
    .split("\n")
    .filter(Boolean);
}
