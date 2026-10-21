// Entry point for agent runs: `tsx src/cli.ts <kind> --input <file.json>`.
// Only usage is implemented so far; see ARCHITECTURE.md for the protocol.
// Nothing but protocol events may go to stdout, so usage goes to stderr.

const KINDS = ["probe", "explore", "triage", "heal"] as const;

const USAGE = `Usage: tsx src/cli.ts <kind> --input <file.json>

Kinds: ${KINDS.join(", ")}

Not implemented yet.`;

function main(argv: string[]): number {
  const [kind] = argv;
  if (kind === undefined || kind === "--help" || kind === "-h") {
    console.error(USAGE);
    return 0;
  }
  console.error(`Unknown or unimplemented command: ${kind}\n\n${USAGE}`);
  return 1;
}

process.exitCode = main(process.argv.slice(2));
