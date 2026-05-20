// auditlogs.js — placeholder until the audit_log table ships with
// Sprint 4 (Observability Foundation). No data is rendered yet; the
// previous illustrative rows were removed so nothing on this view
// implies a working backend.

import { h, mount } from "../lib/dom.js";
import { pageHeader, emptyState, statusBadge } from "../components/common.js";

export default async function auditLogsView({ container }) {
  mount(container,
    pageHeader("Audit Logs", h("span", null,
      "Identity-relevant events. Backed by the audit_log table arriving in Sprint 4 (Observability). ",
      statusBadge("coming-soon"),
    )),
    emptyState({
      title: "Not available yet",
      body: "The audit_log table and auth.EventHook ship in Sprint 4 (Observability Foundation). Once wired, this view will stream identity-relevant events (token validations, forbidden access, header errors).",
    }),
  );
}
