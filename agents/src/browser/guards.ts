// Destructive-action guard: the Explorer and the probe never trigger
// delete, pay, send and similar actions unless the app has
// allow_destructive=true (CLAUDE.md agent rules). Detection is by the
// control's accessible name.

const DESTRUCTIVE: readonly RegExp[] = [
  /\bdelete\b/i,
  /\bremove (my )?account\b/i,
  /\bpay\b/i,
  /\bpurchase\b/i,
  /\bplace (the |my )?order\b/i,
  /\bsend\b/i,
];

// destructiveMatch returns the matched words, or undefined when the name
// looks safe.
export function destructiveMatch(name: string): string | undefined {
  for (const re of DESTRUCTIVE) {
    const m = re.exec(name);
    if (m) return m[0];
  }
  return undefined;
}

export class DestructiveActionBlocked extends Error {
  constructor(name: string, match: string) {
    super(`blocked: "${name}" looks destructive ("${match}") and this app does not allow destructive actions`);
    this.name = "DestructiveActionBlocked";
  }
}

// assertSafe throws unless every given name is safe or destructive actions
// are allowed. Callers pass both the name the model asked for and the
// element's actual name, since a loose match can find a different control.
export function assertSafe(names: string[], allowDestructive: boolean): void {
  if (allowDestructive) return;
  for (const name of names) {
    const match = destructiveMatch(name);
    if (match !== undefined) throw new DestructiveActionBlocked(name, match);
  }
}
