// In-memory sessions, carts and orders, plus the shop logic that planted bugs
// change. Everything resets when the server restarts.
import { randomUUID } from "node:crypto";
import { PRODUCTS, findProduct, type Product } from "./catalog.ts";
import type { BugName } from "./config.ts";

export const TAX_RATE = 0.08;

export interface Session {
  username: string;
  // productId -> quantity, in the order items were first added.
  cart: Map<number, number>;
}

export interface CartLine {
  product: Product;
  quantity: number;
  lineCents: number;
}

export interface Totals {
  subtotalCents: number;
  taxCents: number;
  totalCents: number;
}

export interface Order {
  id: number;
  sessionId: string;
  name: string;
  address: string;
  zip: string;
  cardLast4: string;
  lines: CartLine[];
  totals: Totals;
}

const sessions = new Map<string, Session>();
const orders = new Map<number, Order>();
let nextOrderId = 1001;

export function createSession(username: string): string {
  const id = randomUUID();
  sessions.set(id, { username, cart: new Map() });
  return id;
}

export function getSession(id: string | undefined): Session | undefined {
  return id === undefined ? undefined : sessions.get(id);
}

export function deleteSession(id: string): void {
  sessions.delete(id);
}

export function addToCart(session: Session, productId: number): void {
  session.cart.set(productId, (session.cart.get(productId) ?? 0) + 1);
}

// A quantity of 0 or less removes the line.
export function setQuantity(session: Session, productId: number, quantity: number): void {
  if (quantity <= 0) {
    session.cart.delete(productId);
  } else {
    session.cart.set(productId, quantity);
  }
}

export function cartLines(session: Session): CartLine[] {
  const lines: CartLine[] = [];
  for (const [productId, quantity] of session.cart) {
    const product = findProduct(productId);
    if (product) lines.push({ product, quantity, lineCents: product.priceCents * quantity });
  }
  return lines;
}

// The number shown in the header badge.
export function badgeCount(session: Session, bugs: Set<BugName>): number {
  let count = 0;
  for (const quantity of session.cart.values()) count += quantity;
  // Bug: the badge gets stuck at 1 once the cart has anything in it.
  if (bugs.has("cart_badge")) return Math.min(count, 1);
  return count;
}

export function cartTotals(lines: CartLine[], bugs: Set<BugName>): Totals {
  const subtotalCents = lines.reduce((sum, line) => sum + line.lineCents, 0);
  const taxCents = Math.round(subtotalCents * TAX_RATE);
  // Bug: tax is shown but left out of the total.
  const totalCents = bugs.has("checkout_total") ? subtotalCents : subtotalCents + taxCents;
  return { subtotalCents, taxCents, totalCents };
}

export type SortKey = "name" | "price";

export function searchProducts(query: string, sort: SortKey, bugs: Set<BugName>): Product[] {
  const q = query.trim();
  const matches = PRODUCTS.filter((p) =>
    // Bug: search becomes case-sensitive.
    bugs.has("search_case") ? p.name.includes(q) : p.name.toLowerCase().includes(q.toLowerCase()),
  );
  if (sort === "name") {
    return matches.sort((a, b) => a.name.localeCompare(b.name));
  }
  if (bugs.has("sort_price")) {
    // Bug: prices are compared as strings, so "129.00" sorts before "24.99".
    return matches.sort((a, b) => (a.priceCents / 100).toFixed(2).localeCompare((b.priceCents / 100).toFixed(2)));
  }
  return matches.sort((a, b) => a.priceCents - b.priceCents);
}

export function placeOrder(
  sessionId: string,
  session: Session,
  details: { name: string; address: string; zip: string; cardNumber: string },
  bugs: Set<BugName>,
): Order {
  const lines = cartLines(session);
  const order: Order = {
    id: nextOrderId++,
    sessionId,
    name: details.name,
    address: details.address,
    zip: details.zip,
    // Only the last four digits are kept; the full number is never stored.
    cardLast4: details.cardNumber.slice(-4),
    lines,
    totals: cartTotals(lines, bugs),
  };
  orders.set(order.id, order);
  session.cart.clear();
  return order;
}

// Orders are only visible to the session that placed them.
export function getOrder(id: number, sessionId: string): Order | undefined {
  const order = orders.get(id);
  return order?.sessionId === sessionId ? order : undefined;
}
