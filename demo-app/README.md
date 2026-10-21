# ShopLite

A small demo shop that Sentinel's agents and evals test against. Express with
server-rendered HTML and a little vanilla JS (`public/app.js`). All state
(sessions, carts, orders) is in memory and resets on restart.

```bash
make demo                      # http://localhost:4000
BUGS=cart_badge,sort_price make demo
UI_VERSION=2 make demo
FLAKY=1 make demo
```

Users: `demo` / `demo123` signs in; `locked` / `demo123` shows "This account is locked".

Pages: `/login`, `/products` (search, sort by name or price), `/products/:id`,
`/cart` (change quantity, remove), `/checkout` (name, address, 5-digit zip,
16-digit card; 8% tax), `/order/:id`, and a "Log out" button in the header.

`GET /__health` returns the active mode, so evals can confirm it before running:

```json
{"ok": true, "bugs": ["cart_badge"], "ui_version": 1, "flaky": false}
```

## Modes
An unknown bug name or a bad `UI_VERSION`/`FLAKY` value stops the server at startup.

| Variable | Effect |
|---|---|
| `BUGS` | Comma list of planted bugs (below) |
| `UI_VERSION=2` | Same behavior, changed UI: renamed buttons, new test ids, search moved into the header. Headings, links and field labels stay the same. Used to evaluate healing |
| `FLAKY=1` | `GET /api/products` (which the product list loads from) waits 4–8 s on about 30% of requests, so it sometimes outlasts Playwright's 5 s `expect` timeout. Used to evaluate flaky triage |
| `PORT` | Default 4000 (evals use 4100) |

### Planted bugs
Each bug changes exactly one behavior; search the code for `bugs.has("<name>")`.

| Bug | Effect |
|---|---|
| `cart_badge` | The header cart badge stays at 1 after the first item |
| `sort_price` | "Price: low to high" compares prices as strings ("129.00" before "24.99") |
| `checkout_total` | Total ignores tax (the tax line is still shown) |
| `search_case` | Search becomes case-sensitive |
| `login_error` | A wrong password shows no error message |
| `form_validation` | Checkout accepts an empty (or invalid) zip code |

### UI version 2 changes
| v1 | v2 |
|---|---|
| Log in / Log out | Sign in / Sign out |
| Add to cart | Add to bag |
| Update / Remove | Save / Delete |
| Checkout | Proceed to payment |
| Place order | Pay now |
| Search on the products page | Search in the header (same "Search products" label) |
| `cart-badge`, `product-card`, `add-to-cart`, `cart-line`, `checkout-total`, `order-number`, `search-input`, `sort-select` | `bag-count`, `item-tile`, `add-to-bag`, `bag-line`, `payment-total`, `confirmation-id`, `header-search`, `sort-by` |

## Manual baseline suite
`agents/tests/shoplite/manual/` has 8 hand-written tests (about an hour's work).
It deliberately doesn't chase the planted bugs, so it's an honest baseline for the
generated suite:

- With `BUGS` unset, all 8 pass.
- Of the six bugs, it catches only `cart_badge`.
- With `UI_VERSION=2`, 7 of the 8 fail with locator errors; search still passes.

```bash
cd agents && BASE_URL=http://localhost:4000 npx playwright test tests/shoplite
```
