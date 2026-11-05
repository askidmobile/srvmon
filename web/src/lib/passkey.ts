// Sign-in with passkeys (WebAuthn). Optional: off until the owner adds a
// passkey in the settings; the password with the app code keeps working.
//
// The server sends WebAuthn options as JSON with base64url byte fields;
// the browser API wants ArrayBuffers, and its answer goes back as JSON.
// The conversion is done here instead of PublicKeyCredential.parse*FromJSON,
// which older Safari lacks.
import { request } from "@/lib/api";

export interface PasskeyView {
  id: number;
  rp_id: string;
  name: string;
  created_at: string;
  last_used_at: string | null;
  here: boolean; // made for the address the panel is opened at
}

export interface PasskeyList {
  passkeys: PasskeyView[] | null;
  host: string;
  max: number;
}

// Why passkeys cannot work in this browser at this address; "" if they can.
export type PasskeyProblem = "" | "passkey_ip" | "passkey_insecure" | "passkey_unsupported";

const ipv4 = /^\d{1,3}(\.\d{1,3}){3}$/;

export function isIPHost(host: string): boolean {
  return ipv4.test(host) || host.includes(":") || host.startsWith("[");
}

export function passkeyProblem(): PasskeyProblem {
  if (isIPHost(location.hostname)) return "passkey_ip";
  if (!window.isSecureContext) return "passkey_insecure";
  if (!window.PublicKeyCredential || !navigator.credentials) return "passkey_unsupported";
  return "";
}

/** Face ID / Touch ID / Windows Hello on this device (a phone or a security key may still work without it). */
export async function hasPlatformAuthenticator(): Promise<boolean> {
  try {
    return (await window.PublicKeyCredential?.isUserVerifyingPlatformAuthenticatorAvailable?.()) ?? false;
  } catch {
    return false;
  }
}

function fromB64u(s: string): ArrayBuffer {
  const b = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
  const out = new Uint8Array(b.length);
  for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i);
  return out.buffer;
}

function toB64u(buf: ArrayBuffer | null | undefined): string | undefined {
  if (!buf) return undefined;
  let s = "";
  for (const c of new Uint8Array(buf)) s += String.fromCharCode(c);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

type JSONDescriptor = { id: string; type: PublicKeyCredentialType; transports?: AuthenticatorTransport[] };

const descriptors = (l?: JSONDescriptor[]): PublicKeyCredentialDescriptor[] | undefined =>
  l?.map((d) => ({ ...d, id: fromB64u(d.id) }));

export interface CreationJSON {
  publicKey: Omit<PublicKeyCredentialCreationOptions, "challenge" | "user" | "excludeCredentials"> & {
    challenge: string;
    user: { id: string; name: string; displayName: string };
    excludeCredentials?: JSONDescriptor[];
  };
}

interface RequestJSON {
  publicKey: Omit<PublicKeyCredentialRequestOptions, "challenge" | "allowCredentials"> & {
    challenge: string;
    allowCredentials?: JSONDescriptor[];
  };
}

function credentialJSON(c: PublicKeyCredential) {
  const response: Record<string, unknown> = { clientDataJSON: toB64u(c.response.clientDataJSON) };
  if ("attestationObject" in c.response) {
    const r = c.response as AuthenticatorAttestationResponse;
    response.attestationObject = toB64u(r.attestationObject);
    response.transports = r.getTransports?.() ?? [];
  } else {
    const r = c.response as AuthenticatorAssertionResponse;
    response.authenticatorData = toB64u(r.authenticatorData);
    response.signature = toB64u(r.signature);
    response.userHandle = toB64u(r.userHandle);
  }
  return {
    id: c.id,
    rawId: toB64u(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    clientExtensionResults: c.getClientExtensionResults(),
    response,
  };
}

// The user closed the system prompt or it timed out: not an error to show.
export function cancelled(e: unknown): boolean {
  return e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError");
}

export const passkeyApi = {
  list: () => request<PasskeyList>("GET", "/api/passkeys"),
  remove: (id: number, code: string) => request<unknown>("DELETE", `/api/passkeys/${id}`, { code }),

  /**
   * Starts adding a passkey: the server checks the authenticator code and
   * returns the options for the browser prompt. Fetched before the click on
   * "add", so the prompt opens right in the click (Safari shows it only
   * within a user gesture).
   */
  addBegin: (code: string, name: string) => request<CreationJSON>("POST", "/api/passkeys/begin", { code, name }),

  /** Shows the system prompt (Face ID, Touch ID, PIN) and saves the passkey. */
  async addFinish(o: CreationJSON) {
    const pk = o.publicKey;
    const cred = (await navigator.credentials.create({
      publicKey: {
        ...pk,
        challenge: fromB64u(pk.challenge),
        user: { ...pk.user, id: fromB64u(pk.user.id) },
        excludeCredentials: descriptors(pk.excludeCredentials),
      },
    })) as PublicKeyCredential | null;
    if (!cred) throw new DOMException("no credential", "NotAllowedError");
    return request<{ id: number }>("POST", "/api/passkeys/finish", credentialJSON(cred));
  },

  /** Signs in with a passkey of this address. */
  async signIn() {
    const o = await request<RequestJSON>("POST", "/api/auth/passkey/begin", {});
    const pk = o.publicKey;
    const cred = (await navigator.credentials.get({
      publicKey: { ...pk, challenge: fromB64u(pk.challenge), allowCredentials: descriptors(pk.allowCredentials) },
    })) as PublicKeyCredential | null;
    if (!cred) throw new DOMException("no credential", "NotAllowedError");
    return request<{ authenticated: boolean }>("POST", "/api/auth/passkey/finish", credentialJSON(cred));
  },
};

/** A default name for a new passkey: the device it is made on. */
export function deviceName(): string {
  const ua = navigator.userAgent;
  if (/iPhone/.test(ua)) return "iPhone";
  if (/iPad/.test(ua)) return "iPad";
  if (/Macintosh/.test(ua)) return "Mac";
  if (/Android/.test(ua)) return "Android";
  if (/Windows/.test(ua)) return "Windows";
  if (/Linux/.test(ua)) return "Linux";
  return "passkey";
}
