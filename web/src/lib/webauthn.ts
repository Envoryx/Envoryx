/**
 * Passkeys in the browser: the server sends the WebAuthn options as JSON (binary fields
 * base64url), the browser wants ArrayBuffers, and the answer goes back as JSON again.
 * Written out by hand rather than with PublicKeyCredential.parseCreationOptionsFromJSON,
 * which older browsers lack.
 */

function fromB64url(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4);
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

function toB64url(buf: ArrayBuffer | null | undefined): string | undefined {
  if (!buf) return undefined;
  const bytes = new Uint8Array(buf);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/**
 * Whether passkeys can work on this page: the browser has WebAuthn, the page is a secure
 * context, and its host is a name (a passkey can't be bound to an IP address).
 */
export function passkeysAvailable(): boolean {
  if (typeof window === "undefined" || !window.isSecureContext || typeof window.PublicKeyCredential === "undefined" || !navigator.credentials) return false;
  const host = window.location.hostname;
  return !/^\d+\.\d+\.\d+\.\d+$/.test(host) && !host.includes(":") && !host.startsWith("[");
}

type Descriptor = { id: string; type: string; transports?: string[] };

function descriptors(list: Descriptor[] | undefined): PublicKeyCredentialDescriptor[] {
  return (list ?? []).map((d) => {
    const out: PublicKeyCredentialDescriptor = { id: fromB64url(d.id), type: "public-key" };
    if (d.transports) out.transports = d.transports as AuthenticatorTransport[];
    return out;
  });
}

/** Creates a passkey with the options of the server and returns the answer for it. */
export async function createPasskey(options: PublicKeyCredentialCreationOptionsJSON): Promise<unknown> {
  const publicKey: PublicKeyCredentialCreationOptions = {
    ...(options as unknown as PublicKeyCredentialCreationOptions),
    challenge: fromB64url(options.challenge),
    user: { ...options.user, id: fromB64url(options.user.id) },
    excludeCredentials: descriptors(options.excludeCredentials as Descriptor[] | undefined),
  };
  const cred = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null;
  if (!cred) throw new Error("no passkey was created");
  const res = cred.response as AuthenticatorAttestationResponse;
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: toB64url(res.clientDataJSON),
      attestationObject: toB64url(res.attestationObject),
      transports: typeof res.getTransports === "function" ? res.getTransports() : undefined,
    },
  };
}

/** Signs in with a passkey: asks the browser for one the options allow. */
export async function getPasskey(options: PublicKeyCredentialRequestOptionsJSON): Promise<unknown> {
  const publicKey: PublicKeyCredentialRequestOptions = {
    ...(options as unknown as PublicKeyCredentialRequestOptions),
    challenge: fromB64url(options.challenge),
    allowCredentials: descriptors(options.allowCredentials as Descriptor[] | undefined),
  };
  const cred = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null;
  if (!cred) throw new Error("no passkey was chosen");
  const res = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: toB64url(res.clientDataJSON),
      authenticatorData: toB64url(res.authenticatorData),
      signature: toB64url(res.signature),
      userHandle: toB64url(res.userHandle),
    },
  };
}

/** Whether an error is the user cancelling the browser's passkey dialog. */
export function passkeyCancelled(err: unknown): boolean {
  return err instanceof DOMException && (err.name === "NotAllowedError" || err.name === "AbortError");
}
