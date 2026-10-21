// Server-rendered HTML. Every value that can come from a user goes through
// escapeHtml. Labels and test ids come from ui.ts so UI_VERSION=2 can swap them.
import { formatPrice, type Product } from "./catalog.ts";
import type { CartLine, Order, SortKey, Totals } from "./state.ts";
import type { UiText } from "./ui.ts";

export function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

// What every page needs to draw the header.
export interface PageContext {
  ui: UiText;
  uiVersion: 1 | 2;
  // Undefined on the login page.
  user?: { username: string; badge: number };
  // Current search, so the v2 header search box keeps it.
  query?: string;
}

function searchForm(ctx: PageContext, query: string, extraClass: string): string {
  return `<form class="search ${extraClass}" role="search" action="/products" method="get">
      <label for="search">Search products</label>
      <input id="search" name="q" type="search" value="${escapeHtml(query)}" data-testid="${ctx.ui.testIds.searchInput}">
      <button type="submit">Search</button>
    </form>`;
}

function header(ctx: PageContext): string {
  if (!ctx.user) {
    return `<header class="site-header"><a class="brand" href="/login">ShopLite</a></header>`;
  }
  const search = ctx.uiVersion === 2 ? searchForm(ctx, ctx.query ?? "", "search-header") : "";
  return `<header class="site-header">
    <a class="brand" href="/products">ShopLite</a>
    ${search}
    <nav aria-label="Main">
      <a href="/products">Products</a>
      <a href="/cart">Cart <span class="badge" data-cart-badge data-testid="${ctx.ui.testIds.cartBadge}">${ctx.user.badge}</span></a>
      <span class="user">Signed in as ${escapeHtml(ctx.user.username)}</span>
      <form action="/logout" method="post"><button type="submit" class="link-button">${ctx.ui.logOut}</button></form>
    </nav>
  </header>`;
}

function layout(ctx: PageContext, title: string, main: string): string {
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${escapeHtml(title)} · ShopLite</title>
  <link rel="stylesheet" href="/static/style.css">
  <script src="/static/app.js" defer></script>
</head>
<body>
  ${header(ctx)}
  <main>
    ${main}
  </main>
  <p class="toast" role="status" id="cart-status"></p>
</body>
</html>
`;
}

function addButton(ctx: PageContext, product: Product): string {
  return `<button type="button" class="primary" data-product-id="${product.id}" data-product-name="${escapeHtml(product.name)}" data-added-label="${ctx.ui.added}" data-testid="${ctx.ui.testIds.addToCart}">${ctx.ui.addToCart}</button>`;
}

function totalsList(ctx: PageContext, totals: Totals): string {
  return `<dl class="totals">
      <div><dt>Subtotal</dt><dd>${formatPrice(totals.subtotalCents)}</dd></div>
      <div><dt>Tax (8%)</dt><dd>${formatPrice(totals.taxCents)}</dd></div>
      <div class="grand"><dt>Total</dt><dd data-testid="${ctx.ui.testIds.checkoutTotal}">${formatPrice(totals.totalCents)}</dd></div>
    </dl>`;
}

export function loginPage(ctx: PageContext, opts: { username?: string; error?: string } = {}): string {
  const error = opts.error ? `<p class="error-box" role="alert">${escapeHtml(opts.error)}</p>` : "";
  return layout(
    ctx,
    "Log in",
    `<section class="card narrow">
      <h1>Welcome back</h1>
      <p class="muted">Demo account: demo / demo123</p>
      ${error}
      <form action="/login" method="post" class="stack">
        <label for="username">Username</label>
        <input id="username" name="username" autocomplete="username" value="${escapeHtml(opts.username ?? "")}">
        <label for="password">Password</label>
        <input id="password" name="password" type="password" autocomplete="current-password">
        <button type="submit" class="primary">${ctx.ui.logIn}</button>
      </form>
    </section>`,
  );
}

export function productsPage(ctx: PageContext, query: string, sort: SortKey): string {
  const search = ctx.uiVersion === 1 ? searchForm(ctx, query, "") : "";
  // In v2 the search box is in the header, so the sort form carries the query.
  const keepQuery = ctx.uiVersion === 2 && query !== "" ? `<input type="hidden" name="q" value="${escapeHtml(query)}">` : "";
  return layout(
    { ...ctx, query },
    "Products",
    `<h1>Products</h1>
    <div class="toolbar">
      ${search}
      <form class="sort" action="/products" method="get">
        ${keepQuery}
        <label for="sort">Sort by</label>
        <select id="sort" name="sort" data-testid="${ctx.ui.testIds.sortSelect}">
          <option value="name"${sort === "name" ? " selected" : ""}>Name</option>
          <option value="price"${sort === "price" ? " selected" : ""}>Price: low to high</option>
        </select>
        <noscript><button type="submit">Apply</button></noscript>
      </form>
    </div>
    <p class="muted" id="product-loading">Loading products…</p>
    <ul class="grid" id="product-list" aria-label="Products"
        data-query="${escapeHtml(query)}" data-sort="${sort}"
        data-add-label="${ctx.ui.addToCart}"
        data-added-label="${ctx.ui.added}"
        data-card-testid="${ctx.ui.testIds.productCard}"
        data-add-testid="${ctx.ui.testIds.addToCart}"></ul>`,
  );
}

export function productPage(ctx: PageContext, product: Product): string {
  return layout(
    ctx,
    product.name,
    `<p><a href="/products">← All products</a></p>
    <article class="card">
      <h1>${escapeHtml(product.name)}</h1>
      <p class="price">${formatPrice(product.priceCents)}</p>
      <p>${escapeHtml(product.description)}</p>
      ${addButton(ctx, product)}
    </article>`,
  );
}

export function cartPage(ctx: PageContext, lines: CartLine[], totals: Totals): string {
  if (lines.length === 0) {
    return layout(
      ctx,
      "Cart",
      `<h1>Your cart</h1>
      <p>Your cart is empty.</p>
      <p><a href="/products">Browse products</a></p>`,
    );
  }
  const rows = lines
    .map(({ product, quantity, lineCents }) => {
      const name = escapeHtml(product.name);
      return `<li class="cart-line" data-testid="${ctx.ui.testIds.cartLine}">
        <div>
          <h2><a href="/products/${product.id}">${name}</a></h2>
          <p class="muted">${formatPrice(product.priceCents)} each</p>
        </div>
        <form action="/cart/update" method="post" class="inline">
          <input type="hidden" name="product_id" value="${product.id}">
          <label for="qty-${product.id}">Quantity for ${name}</label>
          <input id="qty-${product.id}" name="quantity" type="number" min="0" max="99" value="${quantity}">
          <button type="submit">${ctx.ui.update}</button>
        </form>
        <form action="/cart/remove" method="post" class="inline">
          <input type="hidden" name="product_id" value="${product.id}">
          <button type="submit" aria-label="${ctx.ui.remove} ${name}">${ctx.ui.remove}</button>
        </form>
        <p class="line-total">${formatPrice(lineCents)}</p>
      </li>`;
    })
    .join("\n");
  return layout(
    ctx,
    "Cart",
    `<h1>Your cart</h1>
    <ul class="cart" aria-label="Cart items">
      ${rows}
    </ul>
    ${totalsList(ctx, totals)}
    <form action="/checkout" method="get">
      <button type="submit" class="primary">${ctx.ui.checkout}</button>
    </form>`,
  );
}

export interface CheckoutValues {
  name: string;
  address: string;
  zip: string;
}

export type CheckoutErrors = Partial<Record<"name" | "address" | "zip" | "card", string>>;

function field(id: string, label: string, value: string, error: string | undefined, attrs = ""): string {
  const described = error ? ` aria-invalid="true" aria-describedby="${id}-error"` : "";
  const message = error ? `<p class="field-error" id="${id}-error">${escapeHtml(error)}</p>` : "";
  return `<div class="field">
        <label for="${id}">${label}</label>
        <input id="${id}" name="${id}" value="${escapeHtml(value)}" aria-required="true"${described} ${attrs}>
        ${message}
      </div>`;
}

export function checkoutPage(
  ctx: PageContext,
  totals: Totals,
  values: CheckoutValues,
  errors: CheckoutErrors = {},
): string {
  const summary =
    Object.keys(errors).length > 0
      ? `<p class="error-box" role="alert">Please fix the errors below.</p>`
      : "";
  return layout(
    ctx,
    "Checkout",
    `<h1>Checkout</h1>
    ${totalsList(ctx, totals)}
    ${summary}
    <form action="/checkout" method="post" class="stack" novalidate>
      ${field("name", "Full name", values.name, errors.name, 'autocomplete="name"')}
      ${field("address", "Address", values.address, errors.address, 'autocomplete="street-address"')}
      ${field("zip", "Zip code", values.zip, errors.zip, 'autocomplete="postal-code" inputmode="numeric"')}
      ${field("card", "Card number", "", errors.card, 'autocomplete="cc-number" inputmode="numeric"')}
      <button type="submit" class="primary">${ctx.ui.placeOrder}</button>
    </form>`,
  );
}

export function orderPage(ctx: PageContext, order: Order): string {
  const items = order.lines
    .map((l) => `<li>${escapeHtml(l.product.name)} × ${l.quantity}: ${formatPrice(l.lineCents)}</li>`)
    .join("\n");
  return layout(
    ctx,
    `Order #${order.id}`,
    `<section class="card">
      <h1>Thank you for your order</h1>
      <p>Order number <strong data-testid="${ctx.ui.testIds.orderNumber}">#${order.id}</strong></p>
      <ul>${items}</ul>
      ${totalsList(ctx, order.totals)}
      <p>Shipping to ${escapeHtml(order.name)}, ${escapeHtml(order.address)}, ${escapeHtml(order.zip)}.</p>
      <p>Paid with card ending ${escapeHtml(order.cardLast4)}.</p>
      <p><a href="/products">Continue shopping</a></p>
    </section>`,
  );
}

export function notFoundPage(ctx: PageContext): string {
  return layout(
    ctx,
    "Not found",
    `<h1>Page not found</h1>
    <p><a href="/products">Back to products</a></p>`,
  );
}
