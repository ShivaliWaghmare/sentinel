// ShopLite: the demo shop used for evals. Placeholder page for now;
// pages, planted bugs (BUGS), UI_VERSION and FLAKY come later (ARCHITECTURE.md).
import express from "express";

const port = Number(process.env.PORT ?? 4000);
const app = express();

app.get("/", (_req, res) => {
  res.type("html").send(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <title>ShopLite</title>
  </head>
  <body>
    <h1>ShopLite</h1>
    <p>Demo shop coming soon.</p>
  </body>
</html>
`);
});

app.listen(port, () => {
  console.log(`ShopLite listening on http://localhost:${port}`);
});
