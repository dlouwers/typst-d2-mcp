import { createHmac } from "node:crypto";
import type { BrowserContext } from "@playwright/test";

// Mirrors SessionCodec in internal/web/session.go:
//   value = base64url(login|unixExpiry) + "." + base64url(hmacSHA256(key, payload))
// Forging the cookie directly is what lets these tests exercise the
// admin UI without standing up a fake GitHub for the OAuth round trip.
// The signing key is fixed by scripts/e2e-server.sh.

const KEY = process.env.E2E_SESSION_KEY ?? "e2e-fixed-session-key";
export const ADMIN_LOGIN = process.env.E2E_ADMIN ?? "e2eadmin";

const b64url = (b: Buffer) => b.toString("base64url");

export function sessionValue(login: string, ttlSeconds = 3600): string {
  const payload = b64url(
    Buffer.from(`${login}|${Math.floor(Date.now() / 1000) + ttlSeconds}`),
  );
  const mac = b64url(createHmac("sha256", KEY).update(payload).digest());
  return `${payload}.${mac}`;
}

/** Give the context a valid admin session for `login`. */
export async function signIn(
  context: BrowserContext,
  origin: string,
  login: string = ADMIN_LOGIN,
): Promise<void> {
  const { hostname } = new URL(origin);
  await context.addCookies([
    {
      name: "ttd2_admin_session",
      value: sessionValue(login),
      domain: hostname,
      path: "/",
      httpOnly: true,
      secure: false,
      sameSite: "Lax",
    },
  ]);
}
