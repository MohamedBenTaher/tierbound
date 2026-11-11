// GitHub App client for the effector (plan step 2.3). Auth is the App flow:
// a short-lived RS256 JWT signed with the App private key buys an
// installation token, which posts the comment. GITHUB_API_URL makes the
// client injectable (the e2e tests point it at a local fake GitHub).

export type Target = { repo: string; issue_number: number };

type AppConfig = {
  apiUrl: string;
  appId: string;
  installationId: string;
  privateKeyPem: string;
  timeoutMs: number;
};

const REPO_RE = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;
const PAGE_SIZE = 100;
const MAX_PAGES = 50;

// SPEC section 7: every comment carries this hidden marker so a retry can
// tell whether an earlier invocation already posted.
export function markerFor(actionId: string): string {
  return `<!-- tierbound:${actionId} -->`;
}

function requireEnv(name: string): string {
  const value = Deno.env.get(name);
  if (!value) throw new Error(`${name} not set`);
  return value;
}

export function timeoutMsFromEnv(): number {
  return Number(Deno.env.get("GITHUB_TIMEOUT_MS") ?? "10000");
}

export function appFromEnv(): GitHubApp {
  return new GitHubApp({
    apiUrl: (Deno.env.get("GITHUB_API_URL") ?? "https://api.github.com").replace(/\/+$/, ""),
    appId: requireEnv("GITHUB_APP_ID"),
    installationId: requireEnv("GITHUB_APP_INSTALLATION_ID"),
    // Secrets set from a file keep real newlines; one-line env values use \n.
    privateKeyPem: requireEnv("GITHUB_APP_PRIVATE_KEY").replaceAll("\\n", "\n"),
    timeoutMs: timeoutMsFromEnv(),
  });
}

export class GitHubApp {
  constructor(private readonly cfg: AppConfig) {}

  // Returns the html_url of the first comment on the issue carrying marker.
  async findMarkedComment(target: Target, marker: string): Promise<string | null> {
    const token = await this.installationToken();
    for (let page = 1; page <= MAX_PAGES; page++) {
      const comments = await this.request<{ body?: string; html_url: string }[]>(
        "GET",
        `${issuePath(target)}/comments?per_page=${PAGE_SIZE}&page=${page}`,
        token,
      );
      const hit = comments.find((c) => c.body?.includes(marker));
      if (hit) return hit.html_url;
      if (comments.length < PAGE_SIZE) return null;
    }
    throw new Error(`marker search exceeded ${MAX_PAGES} pages`);
  }

  async postComment(target: Target, body: string): Promise<string> {
    const token = await this.installationToken();
    const created = await this.request<{ html_url: string }>(
      "POST",
      `${issuePath(target)}/comments`,
      token,
      { body },
    );
    return created.html_url;
  }

  private async installationToken(): Promise<string> {
    const jwt = await appJwt(this.cfg.appId, this.cfg.privateKeyPem);
    const res = await this.request<{ token: string }>(
      "POST",
      `/app/installations/${encodeURIComponent(this.cfg.installationId)}/access_tokens`,
      jwt,
    );
    return res.token;
  }

  private async request<T>(method: string, path: string, bearer: string, body?: unknown): Promise<T> {
    const res = await fetch(`${this.cfg.apiUrl}${path}`, {
      method,
      headers: {
        Accept: "application/vnd.github+json",
        Authorization: `Bearer ${bearer}`,
        "Content-Type": "application/json",
        "User-Agent": "tierbound-effector",
        "X-GitHub-Api-Version": "2022-11-28",
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(this.cfg.timeoutMs),
    });
    if (!res.ok) {
      throw new Error(`GitHub ${method} ${path.split("?")[0]} -> ${res.status}`);
    }
    return await res.json() as T;
  }
}

function issuePath(target: Target): string {
  if (!REPO_RE.test(target.repo)) throw new Error(`invalid repo ${target.repo}`);
  if (!Number.isInteger(target.issue_number) || target.issue_number < 1) {
    throw new Error(`invalid issue_number ${target.issue_number}`);
  }
  return `/repos/${target.repo}/issues/${target.issue_number}`;
}

// --- App JWT (RS256) ------------------------------------------------------

function base64url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_")
    .replace(/=+$/, "");
}

async function appJwt(appId: string, pem: string): Promise<string> {
  const now = Math.floor(Date.now() / 1000);
  const enc = new TextEncoder();
  // GitHub allows 10 minutes; backdate iat for clock drift (GitHub docs).
  const header = base64url(enc.encode(JSON.stringify({ alg: "RS256", typ: "JWT" })));
  const claims = base64url(enc.encode(JSON.stringify({ iat: now - 60, exp: now + 540, iss: appId })));
  const key = await crypto.subtle.importKey(
    "pkcs8",
    pemToPkcs8(pem),
    { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const signingInput = `${header}.${claims}`;
  const sig = await crypto.subtle.sign("RSASSA-PKCS1-v1_5", key, enc.encode(signingInput));
  return `${signingInput}.${base64url(new Uint8Array(sig))}`;
}

// GitHub hands out PKCS#1 ("RSA PRIVATE KEY") PEMs; WebCrypto only imports
// PKCS#8, so wrap PKCS#1 in the PKCS#8 envelope (RFC 5208).
function pemToPkcs8(pem: string): Uint8Array {
  const b64 = pem.replace(/-----[A-Z ]+-----/g, "").replace(/\s+/g, "");
  const der = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
  if (!pem.includes("RSA PRIVATE KEY")) return der;
  const rsaAlgorithm = Uint8Array.of(
    0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00,
  );
  const version = Uint8Array.of(0x02, 0x01, 0x00);
  return derTag(0x30, concat(version, rsaAlgorithm, derTag(0x04, der)));
}

function derTag(tag: number, content: Uint8Array): Uint8Array {
  const len = content.length;
  const lenBytes: number[] = [];
  if (len < 0x80) {
    lenBytes.push(len);
  } else {
    for (let n = len; n > 0; n >>= 8) lenBytes.unshift(n & 0xff);
    lenBytes.unshift(0x80 | lenBytes.length);
  }
  return concat(Uint8Array.of(tag, ...lenBytes), content);
}

function concat(...parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let offset = 0;
  for (const p of parts) {
    out.set(p, offset);
    offset += p.length;
  }
  return out;
}
