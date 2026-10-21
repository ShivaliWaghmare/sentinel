// ShopLite: the demo shop used for evals. Modes (BUGS, UI_VERSION, FLAKY)
// are described in README.md and ARCHITECTURE.md.
import express, { type NextFunction, type Request, type Response } from "express";
import { join } from "node:path";
import { USERS, findProduct } from "./src/catalog.ts";
import { loadConfig } from "./src/config.ts";
import {
  addToCart,
  badgeCount,
  cartLines,
  cartTotals,
  createSession,
  deleteSession,
  getOrder,
  getSession,
  placeOrder,
  searchProducts,
  setQuantity,
  type Session,
  type SortKey,
} from "./src/state.ts";
import { uiText } from "./src/ui.ts";
import {
  cartPage,
  checkoutPage,
  loginPage,
  notFoundPage,
  orderPage,
  productPage,
  productsPage,
  type CheckoutErrors,
  type PageContext,
} from "./src/views.ts";

let config;
try {
  config = loadConfig(process.env);
} catch (err) {
  console.error(err instanceof Error ? err.message : err);
  process.exit(1);
}
const { bugs, uiVersion, flaky } = config;
const ui = uiText(uiVersion);

const SESSION_COOKIE = "sid";

const app = express();
app.use(express.urlencoded({ extended: false }));
app.use(express.json());
app.use("/static", express.static(join(import.meta.dirname, "public")));

// Reads one cookie without pulling in cookie-parser.
function readCookie(req: Request, name: string): string | undefined {
  for (const part of (req.headers.cookie ?? "").split(";")) {
    const [key, ...rest] = part.trim().split("=");
    if (key === name) return decodeURIComponent(rest.join("="));
  }
  return undefined;
}

interface Current {
  sessionId: string;
  session: Session;
}

// The signed-in session, or undefined.
function current(req: Request): Current | undefined {
  const sessionId = readCookie(req, SESSION_COOKIE);
  const session = getSession(sessionId);
  return sessionId !== undefined && session ? { sessionId, session } : undefined;
}

// For handlers behind requireLogin, where a session is guaranteed.
function signedIn(req: Request): Current {
  const cur = current(req);
  if (!cur) throw new Error("signedIn called without a session");
  return cur;
}

function pageContext(cur: Current | undefined): PageContext {
  if (!cur) return { ui, uiVersion };
  return { ui, uiVersion, user: { username: cur.session.username, badge: badgeCount(cur.session, bugs) } };
}

// Form and query values arrive as unknown; anything that isn't a string becomes "".
function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function parseSort(value: unknown): SortKey {
  return value === "price" ? "price" : "name";
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// Pages and APIs below this need a signed-in user.
function requireLogin(req: Request, res: Response, next: NextFunction): void {
  if (current(req)) {
    next();
  } else if (req.path.startsWith("/api/")) {
    res.status(401).json({ error: "not signed in" });
  } else {
    res.redirect("/login");
  }
}

app.get("/__health", (_req, res) => {
  res.json({ ok: true, bugs: [...bugs], ui_version: uiVersion, flaky });
});

app.get("/login", (req, res) => {
  if (current(req)) {
    res.redirect("/products");
    return;
  }
  res.type("html").send(loginPage(pageContext(undefined)));
});

app.post("/login", (req, res) => {
  const username = str(req.body?.username).trim();
  const password = str(req.body?.password);
  const user = USERS.find((u) => u.username === username && u.password === password);
  if (!user) {
    // Bug: a wrong password silently re-renders the form with no message.
    const error = bugs.has("login_error") ? undefined : "Invalid username or password.";
    res.status(401).type("html").send(loginPage(pageContext(undefined), { username, error }));
    return;
  }
  if (user.locked) {
    const error = "This account is locked. Contact support to unlock it.";
    res.status(403).type("html").send(loginPage(pageContext(undefined), { username, error }));
    return;
  }
  const sessionId = createSession(user.username);
  res.cookie(SESSION_COOKIE, sessionId, { httpOnly: true, sameSite: "lax", path: "/" });
  res.redirect("/products");
});

app.post("/logout", (req, res) => {
  const cur = current(req);
  if (cur) deleteSession(cur.sessionId);
  res.clearCookie(SESSION_COOKIE, { path: "/" });
  res.redirect("/login");
});

app.use(requireLogin);

app.get("/", (_req, res) => {
  res.redirect("/products");
});

app.get("/products", (req, res) => {
  res.type("html").send(productsPage(pageContext(signedIn(req)), str(req.query.q), parseSort(req.query.sort)));
});

// The product list is loaded by public/app.js from here; FLAKY slows it down.
app.get("/api/products", async (req, res) => {
  if (flaky && Math.random() < 0.3) {
    await sleep(4000 + Math.random() * 4000);
  }
  const products = searchProducts(str(req.query.q), parseSort(req.query.sort), bugs);
  res.json({
    products: products.map((p) => ({ id: p.id, name: p.name, price_cents: p.priceCents })),
  });
});

app.get("/products/:id", (req, res) => {
  const cur = signedIn(req);
  const product = findProduct(Number(req.params.id));
  if (!product) {
    res.status(404).type("html").send(notFoundPage(pageContext(cur)));
    return;
  }
  res.type("html").send(productPage(pageContext(cur), product));
});

app.post("/api/cart", (req, res) => {
  const cur = signedIn(req);
  const product = findProduct(Number(req.body?.product_id));
  if (!product) {
    res.status(400).json({ error: "unknown product" });
    return;
  }
  addToCart(cur.session, product.id);
  res.json({ badge: badgeCount(cur.session, bugs) });
});

app.get("/cart", (req, res) => {
  const cur = signedIn(req);
  const lines = cartLines(cur.session);
  res.type("html").send(cartPage(pageContext(cur), lines, cartTotals(lines, bugs)));
});

app.post("/cart/update", (req, res) => {
  const cur = signedIn(req);
  const quantity = Number(req.body?.quantity);
  if (Number.isInteger(quantity)) {
    setQuantity(cur.session, Number(req.body?.product_id), Math.min(quantity, 99));
  }
  res.redirect("/cart");
});

app.post("/cart/remove", (req, res) => {
  const cur = signedIn(req);
  setQuantity(cur.session, Number(req.body?.product_id), 0);
  res.redirect("/cart");
});

app.get("/checkout", (req, res) => {
  const cur = signedIn(req);
  const lines = cartLines(cur.session);
  if (lines.length === 0) {
    res.redirect("/cart");
    return;
  }
  res.type("html").send(checkoutPage(pageContext(cur), cartTotals(lines, bugs), { name: "", address: "", zip: "" }));
});

app.post("/checkout", (req, res) => {
  const cur = signedIn(req);
  const lines = cartLines(cur.session);
  if (lines.length === 0) {
    res.redirect("/cart");
    return;
  }
  const values = {
    name: str(req.body?.name).trim(),
    address: str(req.body?.address).trim(),
    zip: str(req.body?.zip).trim(),
  };
  const cardNumber = str(req.body?.card).replaceAll(" ", "");

  const errors: CheckoutErrors = {};
  if (values.name === "") errors.name = "Enter your full name.";
  if (values.address === "") errors.address = "Enter your address.";
  // Bug: the zip check is skipped, so an empty zip is accepted.
  if (!bugs.has("form_validation") && !/^\d{5}$/.test(values.zip)) errors.zip = "Enter a 5-digit zip code.";
  if (!/^\d{16}$/.test(cardNumber)) errors.card = "Enter a 16-digit card number.";

  if (Object.keys(errors).length > 0) {
    res.status(400).type("html").send(checkoutPage(pageContext(cur), cartTotals(lines, bugs), values, errors));
    return;
  }
  const order = placeOrder(cur.sessionId, cur.session, { ...values, cardNumber }, bugs);
  res.redirect(`/order/${order.id}`);
});

app.get("/order/:id", (req, res) => {
  const cur = signedIn(req);
  const order = getOrder(Number(req.params.id), cur.sessionId);
  if (!order) {
    res.status(404).type("html").send(notFoundPage(pageContext(cur)));
    return;
  }
  res.type("html").send(orderPage(pageContext(cur), order));
});

app.use((req, res) => {
  res.status(404).type("html").send(notFoundPage(pageContext(current(req))));
});

app.listen(config.port, () => {
  const mode = `bugs=[${[...bugs].join(",")}] ui_version=${uiVersion} flaky=${flaky}`;
  console.log(`ShopLite listening on http://localhost:${config.port} (${mode})`);
});
