/*
 * Interface de consultation. Tout est en lecture : la page interroge l'API JSON
 * et rend le résultat, elle n'écrit rien. Les valeurs venant de la base sont
 * insérées via textContent, jamais via innerHTML, parce que l'e-mail et le nom
 * d'un client OAuth sont des données saisies ailleurs.
 */

// Libellés des signaux, renseignés par /api/status.
let SIGNAL_NAMES = {};

const state = {
  accounts: [],
  sort: "last_seen_at",
  order: "desc",
  timer: null,
};

const RELATIVE = new Intl.RelativeTimeFormat("fr", { numeric: "auto" });
const ABSOLUTE = new Intl.DateTimeFormat("fr-FR", { dateStyle: "medium", timeStyle: "short" });

const APPROVAL_LABELS = {
  pending: "En attente",
  approved: "Validé",
  blocked: "Bloqué",
};

const STATUS_LABELS = {
  actif: "Actif",
  inactif: "Inactif",
  dormant: "Dormant",
  jamais_connecte: "Jamais connecté",
};

const el = (id) => document.getElementById(id);

/* -- utilitaires d'affichage ---------------------------------------------- */

function relative(iso) {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";
  const seconds = (then - Date.now()) / 1000;
  const steps = [
    [60, "second"],
    [3600, "minute"],
    [86400, "hour"],
    [2592000, "day"],
    [31536000, "month"],
    [Infinity, "year"],
  ];
  const divisors = { second: 1, minute: 60, hour: 3600, day: 86400, month: 2592000, year: 31536000 };
  const step = steps.find(([limit]) => Math.abs(seconds) < limit);
  const unit = step ? step[1] : "year";
  return RELATIVE.format(Math.round(seconds / divisors[unit]), unit);
}

function absolute(iso) {
  if (!iso) return "—";
  const moment = new Date(iso);
  return Number.isNaN(moment.getTime()) ? "—" : ABSOLUTE.format(moment);
}

function badge(status) {
  const span = document.createElement("span");
  span.className = `badge badge--${status}`;
  span.textContent = STATUS_LABELS[status] || status;
  return span;
}

function approvalBadge(state) {
  const span = document.createElement("span");
  span.className = `badge badge--${state}`;
  span.textContent = APPROVAL_LABELS[state] || state;
  return span;
}

// Un bouton d'action dans une ligne cliquable : le clic ne doit pas aussi ouvrir le
// détail, d'où l'arrêt de la propagation.
function actionButton(label, action) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "button button--small";
  button.textContent = label;
  button.addEventListener("click", (event) => {
    event.stopPropagation();
    action();
  });
  return button;
}

async function decide(id, state, note = "") {
  try {
    const response = await fetch(`/api/accounts/${encodeURIComponent(id)}/approval`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify({ state, note }),
    });
    if (!response.ok) {
      let detail = `HTTP ${response.status}`;
      try {
        const body = await response.json();
        if (body && body.detail) detail = body.detail;
      } catch (error) {
        /* réponse non JSON : on garde le code HTTP */
      }
      throw new Error(detail);
    }
    showBanner("");
    const dialog = el("detail");
    if (dialog.open) dialog.close();
    await load();
  } catch (error) {
    showBanner(error.message);
  }
}

function cell(row, text, className) {
  const td = document.createElement("td");
  if (className) td.className = className;
  td.textContent = text;
  row.appendChild(td);
  return td;
}

function showBanner(message) {
  const banner = el("banner");
  banner.textContent = message;
  banner.hidden = !message;
}

/* -- appels API ------------------------------------------------------------ */

async function api(path) {
  const response = await fetch(path, { headers: { Accept: "application/json" } });
  if (response.status === 401) {
    throw new Error("Authentification refusée. Rechargez la page pour saisir vos identifiants.");
  }
  if (!response.ok) {
    let detail = `HTTP ${response.status}`;
    try {
      const body = await response.json();
      if (body && body.detail) detail = body.detail;
    } catch (error) {
      /* réponse non JSON : on garde le code HTTP */
    }
    throw new Error(detail);
  }
  return response.json();
}

/* -- rendu ----------------------------------------------------------------- */

function renderTiles(stats) {
  const tiles = [
    ["Comptes créés", stats.accounts_total],
    ["Liés à Garmin", stats.garmin_linked],
    [`Actifs (≤ ${stats.thresholds.active_days} j)`, stats.by_status.actif],
    ["Vus sous 24 h", stats.seen_last_24_hours],
    ["En attente de validation", stats.approval_pending],
    ["Notice acceptée", stats.privacy_consent],
    ["Sans consentement", stats.privacy_consent_missing],
    ["Jamais connectés", stats.by_status.jamais_connecte],
  ];
  const container = el("tiles");
  container.textContent = "";
  for (const [label, value] of tiles) {
    const tile = document.createElement("div");
    tile.className = "tile";
    const number = document.createElement("div");
    number.className = "tile__value";
    number.textContent = String(value ?? 0);
    const caption = document.createElement("div");
    caption.className = "tile__label";
    caption.textContent = label;
    tile.append(number, caption);
    container.appendChild(tile);
  }
}

function renderRows(accounts) {
  const body = el("rows");
  body.textContent = "";
  el("result-count").textContent = `${accounts.length} compte${accounts.length > 1 ? "s" : ""}`;

  if (accounts.length === 0) {
    const row = document.createElement("tr");
    row.className = "empty";
    const td = document.createElement("td");
    td.colSpan = 8;
    td.textContent = "Aucun compte ne correspond aux filtres.";
    row.appendChild(td);
    body.appendChild(row);
    return;
  }

  for (const account of accounts) {
    const row = document.createElement("tr");
    row.tabIndex = 0;
    row.dataset.id = account.id;

    const identity = document.createElement("td");
    const wrapper = document.createElement("div");
    wrapper.className = "identity";
    const email = document.createElement("strong");
    email.textContent = account.email || "(aucun e-mail)";
    const sub = document.createElement("span");
    sub.textContent = account.garmin_linked ? "Garmin lié" : "Garmin non lié";
    wrapper.append(email, sub);
    identity.appendChild(wrapper);
    row.appendChild(identity);

    const approval = document.createElement("td");
    approval.appendChild(approvalBadge(account.approval_state));
    if (account.approval_state === "pending") {
      const actions = document.createElement("div");
      actions.className = "row-actions";
      actions.append(
        actionButton("Valider", () => decide(account.id, "approved")),
        actionButton("Bloquer", () => decide(account.id, "blocked")),
      );
      approval.appendChild(actions);
    }
    row.appendChild(approval);

    const statusCell = document.createElement("td");
    statusCell.appendChild(badge(account.status));
    row.appendChild(statusCell);

    const created = document.createElement("td");
    created.appendChild(document.createTextNode(absolute(account.created_at)));
    const createdRelative = document.createElement("div");
    createdRelative.className = "secondary";
    createdRelative.textContent = relative(account.created_at);
    created.appendChild(createdRelative);
    row.appendChild(created);

    const seen = document.createElement("td");
    seen.appendChild(document.createTextNode(relative(account.last_seen_at)));
    const seenAbsolute = document.createElement("div");
    seenAbsolute.className = "secondary";
    seenAbsolute.textContent = absolute(account.last_seen_at);
    seen.appendChild(seenAbsolute);
    row.appendChild(seen);

    cell(row, account.last_seen_label || "—", "secondary");

    const consent = document.createElement("td");
    if (account.privacy_accepted_at) {
      consent.appendChild(document.createTextNode(absolute(account.privacy_accepted_at)));
      const version = document.createElement("div");
      version.className = "secondary";
      version.textContent = `version ${account.privacy_notice_version || "?"}`;
      consent.appendChild(version);
    } else {
      const none = document.createElement("span");
      none.className = "badge badge--dormant";
      none.textContent = "Aucun";
      consent.appendChild(none);
    }
    row.appendChild(consent);

    const clients = document.createElement("td");
    const plural = (count, word) => `${count} ${word}${count > 1 ? "s" : ""}`;
    clients.appendChild(document.createTextNode(plural(account.clients_authorized, "client")));
    const families = document.createElement("div");
    families.className = "secondary";
    families.textContent = `${plural(account.token_families_active, "famille")} active${
      account.token_families_active > 1 ? "s" : ""
    }`;
    clients.appendChild(families);
    row.appendChild(clients);


    row.addEventListener("click", () => openDetail(account.id));
    row.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        openDetail(account.id);
      }
    });
    body.appendChild(row);
  }
}

function table(headers, rows) {
  const element = document.createElement("table");
  const head = element.createTHead().insertRow();
  for (const header of headers) {
    const th = document.createElement("th");
    th.textContent = header;
    head.appendChild(th);
  }
  const tbody = element.createTBody();
  if (rows.length === 0) {
    const cellElement = tbody.insertRow().insertCell();
    cellElement.colSpan = headers.length;
    cellElement.textContent = "Aucune donnée.";
    return element;
  }
  for (const values of rows) {
    const row = tbody.insertRow();
    for (const value of values) {
      row.insertCell().textContent = value ?? "—";
    }
  }
  return element;
}

async function openDetail(id) {
  const dialog = el("detail");
  const body = el("detail-body");
  body.textContent = "Chargement…";
  dialog.showModal();

  let account;
  try {
    account = await api(`/api/accounts/${encodeURIComponent(id)}`);
  } catch (error) {
    body.textContent = error.message;
    return;
  }

  body.textContent = "";
  const title = document.createElement("h2");
  title.textContent = account.email || "(aucun e-mail)";
  const subtitle = document.createElement("p");
  subtitle.className = "mono secondary";
  subtitle.textContent = account.id;
  body.append(title, subtitle);

  const summary = document.createElement("p");
  summary.append(badge(account.status));
  const summaryText = document.createElement("span");
  summaryText.textContent = ` Dernière connexion ${relative(account.last_seen_at)} (${absolute(
    account.last_seen_at,
  )}) — ${account.last_seen_label || "aucun signal"}`;
  summary.appendChild(summaryText);
  body.appendChild(summary);

  const signals = document.createElement("h3");
  signals.textContent = "Signaux horodatés";
  body.append(
    signals,
    table(
      ["Signal", "Date"],
      Object.entries(account.signals).map(([key, value]) => [
        SIGNAL_NAMES[key] || key,
        value ? absolute(value) : "—",
      ]),
    ),
  );

  const decision = document.createElement("h3");
  decision.textContent = "Validation du compte";
  const decisionState = document.createElement("p");
  decisionState.append(approvalBadge(account.approval_state));
  const decisionDetail = document.createElement("span");
  decisionDetail.textContent = account.approval_decided_at
    ? ` décidé le ${absolute(account.approval_decided_at)} par ${account.approval_decided_by || "?"}${
        account.approval_note ? ` — ${account.approval_note}` : ""
      }`
    : " aucune décision enregistrée : le compte ne peut rien faire tant qu'il attend.";
  decisionState.appendChild(decisionDetail);

  const decisionActions = document.createElement("p");
  decisionActions.className = "row-actions";
  for (const [label, state] of [
    ["Valider", "approved"],
    ["Bloquer", "blocked"],
    ["Remettre en attente", "pending"],
  ]) {
    if (state === account.approval_state) continue;
    decisionActions.appendChild(actionButton(label, () => decide(account.id, state)));
  }
  body.append(decision, decisionState, decisionActions);

  const privacy = document.createElement("h3");
  privacy.textContent = "Notice de confidentialité";
  body.append(
    privacy,
    table(
      ["Version", "Acceptée le", "Empreinte du texte"],
      (account.privacy_notice_consents || []).map((acceptance) => [
        acceptance.notice_version,
        absolute(acceptance.accepted_at),
        // L'empreinte identifie le texte accepté ; les douze premiers caractères
        // suffisent à distinguer deux versions à l'œil.
        `${(acceptance.notice_hash || "").slice(0, 12)}…`,
      ]),
    ),
  );

  const consents = document.createElement("h3");
  consents.textContent = "Clients autorisés";
  body.append(
    consents,
    table(
      ["Client", "Portées", "Accordé le", "Révoqué le"],
      account.consents.map((consent) => [
        consent.client_name || consent.client_id,
        consent.scopes || "—",
        absolute(consent.granted_at),
        consent.revoked_at ? absolute(consent.revoked_at) : "—",
      ]),
    ),
  );

  const families = document.createElement("h3");
  families.textContent = "Familles de jetons";
  body.append(
    families,
    table(
      ["Client", "Créée le", "Dernier jeton", "Jetons", "Révoquée"],
      account.token_families.map((family) => [
        family.client_name || family.client_id,
        absolute(family.created_at),
        family.last_token_issued_at ? absolute(family.last_token_issued_at) : "—",
        String(family.tokens),
        family.revoked_at ? `${absolute(family.revoked_at)} (${family.revocation_reason || "—"})` : "non",
      ]),
    ),
  );

  if (account.audit_events.length > 0) {
    const audit = document.createElement("h3");
    audit.textContent = "Événements d'audit";
    body.append(
      audit,
      table(
        ["Date", "Type", "Issue", "Détail"],
        account.audit_events.map((event) => [
          absolute(event.occurred_at),
          event.kind,
          event.outcome,
          event.detail || "—",
        ]),
      ),
    );
  }
}

/* -- chargement ------------------------------------------------------------ */

function queryString() {
  const params = new URLSearchParams();
  const search = el("search").value.trim();
  if (search) params.set("search", search);
  if (el("status").value) params.set("status", el("status").value);
  if (el("linked").value) params.set("linked", el("linked").value);
  if (el("consent").value) params.set("consent", el("consent").value);
  if (el("approval").value) params.set("approval", el("approval").value);
  params.set("sort", state.sort);
  params.set("order", state.order);
  params.set("limit", "1000");
  return params;
}

async function load() {
  try {
    const params = queryString();
    const [stats, page] = await Promise.all([api("/api/stats"), api(`/api/accounts?${params}`)]);
    showBanner("");
    renderTiles(stats);
    state.accounts = page.items;
    renderRows(page.items);

    const exportParams = new URLSearchParams(params);
    exportParams.delete("sort");
    exportParams.delete("order");
    exportParams.delete("limit");
    el("export-csv").href = `/api/accounts.csv?${exportParams}`;
  } catch (error) {
    showBanner(error.message);
    el("rows").textContent = "";
  }
}

async function loadStatus() {
  try {
    const status = await api("/api/status");
    SIGNAL_NAMES = status.signals || {};
    document.title = `${status.settings.title || "garmin-mcp"} — comptes`;
    el("page-title").textContent = status.settings.title || "Comptes garmin-mcp";
    const mode = status.database.access_mode === "snapshot" ? "instantané" : "directe";
    el("source-line").textContent =
      `Base ${status.database.path} — lecture ${mode}, ` +
      `modifiée ${relative(status.database.modified_at)} · ` +
      `actif ≤ ${status.settings.active_days} j, dormant > ${status.settings.idle_days} j` +
      (status.settings.mask_emails ? " · e-mails masqués" : "");
  } catch (error) {
    showBanner(error.message);
  }
}

function bind() {
  el("refresh").addEventListener("click", load);

  let debounce;
  el("search").addEventListener("input", () => {
    clearTimeout(debounce);
    debounce = setTimeout(load, 250);
  });
  el("status").addEventListener("change", load);
  el("linked").addEventListener("change", load);
  el("consent").addEventListener("change", load);
  el("approval").addEventListener("change", load);

  for (const button of document.querySelectorAll("thead button[data-sort]")) {
    button.addEventListener("click", () => {
      const field = button.dataset.sort;
      if (state.sort === field) {
        state.order = state.order === "desc" ? "asc" : "desc";
      } else {
        state.sort = field;
        state.order = "desc";
      }
      for (const other of document.querySelectorAll("thead button[data-sort]")) {
        other.removeAttribute("aria-sort");
      }
      button.setAttribute("aria-sort", state.order === "desc" ? "descending" : "ascending");
      load();
    });
  }

  el("auto-refresh").addEventListener("change", (event) => {
    clearInterval(state.timer);
    state.timer = event.target.checked ? setInterval(load, 60000) : null;
  });
}

bind();
loadStatus().then(load);
