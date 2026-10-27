// Helpers for external text (pages, test output, tool results). Everything
// the model reads from outside goes through untrusted(), and the system
// prompts tell the model never to follow instructions inside those tags.

export function truncate(text: string, maxChars: number): string {
  if (text.length <= maxChars) return text;
  return `${text.slice(0, maxChars)}\n…[truncated ${text.length - maxChars} characters]`;
}

// untrusted wraps text in <untrusted source="..."> tags, truncated to
// maxChars. A closing tag inside the text is neutralized so the content
// can't end the block early and pose as instructions.
export function untrusted(source: string, text: string, maxChars = 4000): string {
  const safeSource = source.replace(/[^\w:./ -]/g, "_");
  const body = truncate(text, maxChars).replace(/<\/untrusted/gi, "<\\/untrusted");
  return `<untrusted source="${safeSource}">\n${body}\n</untrusted>`;
}
