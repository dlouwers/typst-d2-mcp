// <sl-table density="comfortable|compact" zebra>
//   <table>
//     <thead><tr><th>Symbol</th><th class="num">P/L</th></tr></thead>
//     <tbody><tr><td>OXY</td><td class="num pos">+635.19</td></tr></tbody>
//   </table>
// </sl-table>
//
// Token-styled wrapper around a native <table>. Light-DOM by design —
// the component's value is in styling the slotted <table>'s descendants
// (<th>, <td>, etc.), and Shadow DOM ::slotted() only matches direct
// children, not their nested elements. Light-DOM lets the rules cascade
// naturally to <th>/<td>/<tr>.
//
// Styles live in a single <style> tag auto-injected into <head> on the
// first sl-table instantiation; subsequent instances reuse it.
//
// Conventions:
// - <th>/<td class="num"> right-aligns and applies tabular numerics
// - <td class="pos">/.neg apply gain/loss colours
// - density="compact" tightens row padding for dense data tables
// - zebra alternates row backgrounds (off by default)

const SL_TABLE_STYLE_ID = "sl-table-styles";

const SL_TABLE_CSS = `
  sl-table {
    display: block;
    font-family: var(--sl-font-family-body, ui-sans-serif, system-ui, sans-serif);
    color: var(--sl-color-ink);
    overflow-x: auto;
    --sl-table-row-py: var(--sl-space-3, 12px);
    --sl-table-row-px: var(--sl-space-4, 16px);
  }
  sl-table[hidden] { display: none; }
  sl-table[density="compact"] {
    --sl-table-row-py: 6px;
    --sl-table-row-px: var(--sl-space-3, 12px);
  }
  sl-table > table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--sl-font-size-base, 14px);
  }
  sl-table > table th,
  sl-table > table td {
    padding: var(--sl-table-row-py) var(--sl-table-row-px);
    border-bottom: 1px solid var(--sl-color-line);
    text-align: left;
    vertical-align: top;
  }
  sl-table > table thead th {
    font-size: var(--sl-font-size-xs, 11px);
    font-weight: var(--sl-font-weight-semibold, 600);
    text-transform: uppercase;
    letter-spacing: var(--sl-font-letter-spacing-wide, 0.06em);
    color: var(--sl-color-muted);
  }
  sl-table > table tbody tr:last-child td {
    border-bottom: none;
  }
  sl-table > table tfoot td {
    border-top: 1px solid var(--sl-color-line);
    border-bottom: none;
    font-weight: var(--sl-font-weight-semibold, 600);
  }
  sl-table > table th.num,
  sl-table > table td.num {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  sl-table > table td.pos,
  sl-table > table th.pos { color: var(--sl-color-gain); }
  sl-table > table td.neg,
  sl-table > table th.neg { color: var(--sl-color-loss); }
  sl-table[zebra] > table tbody tr:nth-child(even) td {
    background: var(--sl-color-paper);
  }
`;

function ensureStyles() {
  if (document.getElementById(SL_TABLE_STYLE_ID)) return;
  const s = document.createElement("style");
  s.id = SL_TABLE_STYLE_ID;
  s.textContent = SL_TABLE_CSS;
  document.head.appendChild(s);
}

class SlTable extends HTMLElement {
  // Light-DOM intentionally — see file-level comment above.
  connectedCallback() {
    ensureStyles();
  }
}

customElements.define("sl-table", SlTable);
