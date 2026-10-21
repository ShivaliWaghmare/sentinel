// Button labels and test ids for each UI version. UI_VERSION=2 changes only
// these (plus moving search into the header), so healing evals have a clear diff.
// v2 labels never contain the v1 label, so substring locators break too.

export interface UiText {
  logIn: string;
  logOut: string;
  addToCart: string;
  update: string;
  remove: string;
  checkout: string;
  placeOrder: string;
  added: string;
  testIds: {
    cartBadge: string;
    productCard: string;
    addToCart: string;
    cartLine: string;
    checkoutTotal: string;
    orderNumber: string;
    searchInput: string;
    sortSelect: string;
  };
}

const V1: UiText = {
  logIn: "Log in",
  logOut: "Log out",
  addToCart: "Add to cart",
  update: "Update",
  remove: "Remove",
  checkout: "Checkout",
  placeOrder: "Place order",
  added: "Added to cart",
  testIds: {
    cartBadge: "cart-badge",
    productCard: "product-card",
    addToCart: "add-to-cart",
    cartLine: "cart-line",
    checkoutTotal: "checkout-total",
    orderNumber: "order-number",
    searchInput: "search-input",
    sortSelect: "sort-select",
  },
};

const V2: UiText = {
  logIn: "Sign in",
  logOut: "Sign out",
  addToCart: "Add to bag",
  update: "Save",
  remove: "Delete",
  checkout: "Proceed to payment",
  placeOrder: "Pay now",
  added: "Added to bag",
  testIds: {
    cartBadge: "bag-count",
    productCard: "item-tile",
    addToCart: "add-to-bag",
    cartLine: "bag-line",
    checkoutTotal: "payment-total",
    orderNumber: "confirmation-id",
    searchInput: "header-search",
    sortSelect: "sort-by",
  },
};

export function uiText(version: 1 | 2): UiText {
  return version === 2 ? V2 : V1;
}
