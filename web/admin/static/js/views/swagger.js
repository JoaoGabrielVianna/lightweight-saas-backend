// swagger.js — embedded Swagger UI via iframe to /swagger/index.html.

import { h, mount } from "../lib/dom.js";
import { pageHeader } from "../components/common.js";

export default async function swaggerView({ container }) {
  mount(container,
    pageHeader("Swagger", "OpenAPI documentation for this API.", [
      h("a", { class: "btn", href: "/swagger/index.html", target: "_blank", rel: "noreferrer" }, "Open in new tab ↗"),
    ]),
    h("div", { class: "card", style: { padding: 0, overflow: "hidden" } },
      h("iframe", {
        src: "/swagger/index.html",
        style: { width: "100%", height: "calc(100vh - 220px)", border: "0", background: "white" },
        title: "Swagger UI",
      }),
    ),
  );
}
