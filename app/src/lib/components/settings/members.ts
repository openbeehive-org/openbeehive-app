// What the member and invite lists show, apart from the components so it
// can be tested without a DOM.

export type Workspace = { id: string; name: string; role: string };

export type Member = {
  user_id: string;
  name: string;
  me: boolean;
  // Only present for the workspace's admins and on one's own entry.
  email?: string;
  role?: string;
};

/** The name to show for a member: their name, else their address, else a stand-in. */
export function memberLabel(m: Member, fallback: string): string {
  return m.name?.trim() || m.email?.trim() || fallback;
}

export type OpenInvite = {
  id: string;
  email: string;
  role: string;
  created_at: string;
  link: string;
  // Servers before 0.5.0 send neither; such invites never expired.
  expires_at?: string;
  expired?: boolean;
};

/** Whether an invite can no longer be used, by the server's word or the clock. */
export function inviteExpired(invite: OpenInvite, now: Date = new Date()): boolean {
  if (invite.expired) return true;
  if (!invite.expires_at) return false;
  const at = new Date(invite.expires_at);
  return !Number.isNaN(at.getTime()) && at <= now;
}

/**
 * Whether what the user typed names the workspace they are about to delete.
 * Exact apart from surrounding spaces: a near miss must not delete anything.
 */
export function namesMatch(typed: string, name: string): boolean {
  return name.trim() !== '' && typed.trim() === name.trim();
}
