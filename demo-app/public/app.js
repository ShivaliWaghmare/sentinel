// ShopLite browser code: renders the product list from /api/products,
// adds items to the cart without a page reload, and auto-submits the sort form.

function formatPrice(cents) {
  return `$${(cents / 100).toFixed(2)}`;
}

function showStatus(text) {
  const status = document.getElementById("cart-status");
  if (!status) return;
  status.textContent = text;
  status.classList.add("visible");
}

async function loadProducts(list) {
  const loading = document.getElementById("product-loading");
  const params = new URLSearchParams({ q: list.dataset.query, sort: list.dataset.sort });
  try {
    const res = await fetch(`/api/products?${params}`);
    if (!res.ok) throw new Error(`status ${res.status}`);
    const { products } = await res.json();
    list.replaceChildren(...products.map((p) => productCard(list.dataset, p)));
    loading.textContent = products.length === 0 ? `No products match "${list.dataset.query}".` : "";
  } catch {
    loading.textContent = "Couldn't load products. Please reload the page.";
    loading.setAttribute("role", "alert");
  }
}

// Builds one card with DOM methods (no innerHTML), using the labels and test
// ids the server put on the list for the current UI version.
function productCard(labels, product) {
  const item = document.createElement("li");
  item.className = "card";
  item.dataset.testid = labels.cardTestid;

  const heading = document.createElement("h2");
  const link = document.createElement("a");
  link.href = `/products/${product.id}`;
  link.textContent = product.name;
  heading.append(link);

  const price = document.createElement("p");
  price.className = "price";
  price.textContent = formatPrice(product.price_cents);

  const button = document.createElement("button");
  button.type = "button";
  button.className = "primary";
  button.dataset.productId = String(product.id);
  button.dataset.productName = product.name;
  button.dataset.addedLabel = labels.addedLabel;
  button.dataset.testid = labels.addTestid;
  button.textContent = labels.addLabel;

  item.append(heading, price, button);
  return item;
}

async function addToCart(button) {
  button.disabled = true;
  try {
    const res = await fetch("/api/cart", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ product_id: Number(button.dataset.productId) }),
    });
    if (!res.ok) throw new Error(`status ${res.status}`);
    const { badge } = await res.json();
    for (const el of document.querySelectorAll("[data-cart-badge]")) el.textContent = String(badge);
    showStatus(`${button.dataset.addedLabel}: ${button.dataset.productName}`);
  } catch {
    showStatus("Something went wrong. Please try again.");
  } finally {
    button.disabled = false;
  }
}

document.addEventListener("click", (event) => {
  const button = event.target instanceof Element ? event.target.closest("button[data-product-id]") : null;
  if (button) addToCart(button);
});

document.addEventListener("DOMContentLoaded", () => {
  const list = document.getElementById("product-list");
  if (list) loadProducts(list);

  const sort = document.getElementById("sort");
  if (sort) sort.addEventListener("change", () => sort.form.requestSubmit());
});
