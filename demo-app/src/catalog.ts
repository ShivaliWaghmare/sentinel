// Static shop data. Prices are integer cents so totals never hit float rounding.

export interface Product {
  id: number;
  name: string;
  priceCents: number;
  description: string;
}

// Prices are chosen so a string sort ("129.00" < "24.99" < "4.75") visibly
// differs from a numeric one; that's what the sort_price bug relies on.
export const PRODUCTS: readonly Product[] = [
  { id: 1, name: "Classic Mug", priceCents: 1200, description: "A 350 ml stoneware mug that keeps coffee warm." },
  { id: 2, name: "Desk Lamp", priceCents: 4999, description: "Adjustable LED lamp with three brightness levels." },
  { id: 3, name: "Notebook", priceCents: 475, description: "A5 dotted notebook with 120 pages." },
  { id: 4, name: "Wireless Mouse", priceCents: 2499, description: "Quiet mouse with a 12-month battery." },
  { id: 5, name: "Mechanical Keyboard", priceCents: 12900, description: "Tenkeyless keyboard with tactile switches." },
  { id: 6, name: "Water Bottle", priceCents: 950, description: "750 ml insulated steel bottle." },
  { id: 7, name: "Laptop Stand", priceCents: 3500, description: "Aluminium stand that raises your screen to eye level." },
  { id: 8, name: "Sticky Notes", priceCents: 725, description: "Pack of six pastel sticky note pads." },
];

export function findProduct(id: number): Product | undefined {
  return PRODUCTS.find((p) => p.id === id);
}

export interface User {
  username: string;
  password: string;
  locked: boolean;
}

export const USERS: readonly User[] = [
  { username: "demo", password: "demo123", locked: false },
  { username: "locked", password: "demo123", locked: true },
];

export function formatPrice(cents: number): string {
  return `$${(cents / 100).toFixed(2)}`;
}
