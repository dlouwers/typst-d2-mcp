// <sl-card padding="md|lg|none" elevation="flat|raised">
//   <h2 slot="header">Title</h2>
//   <span slot="actions">…</span>
//   body content
// </sl-card>
//
// Surface container — the brand's basic card chrome (background, border,
// radius, optional shadow). Three slots: default (body), "header"
// (top-of-card title row), "actions" (footer button row). All optional;
// a card with only a default slot just renders the surface around it.
//
// `padding` controls the internal padding scale ("md" default, "lg"
// for hero cards, "none" for tables/lists that need flush edges).
// `elevation` controls shadow depth ("flat" default, "raised" for
// elevated dialogs/popovers).

class SlCard extends HTMLElement {
  static observedAttributes = ["padding", "elevation"];

  constructor() {
    super();
    const root = this.attachShadow({ mode: "open" });
    root.innerHTML = `
      <style>
        :host {
          display: block;
          background: var(--sl-color-surface);
          border: 1px solid var(--sl-color-line);
          border-radius: var(--sl-radius-lg, 6px);
          font-family: var(--sl-font-family-body, ui-sans-serif, system-ui, sans-serif);
          color: var(--sl-color-ink);
        }
        :host([hidden]) { display: none; }

        :host([elevation="raised"]) {
          box-shadow: var(--sl-shadow-md);
        }

        .surface {
          display: grid;
          grid-template-rows: auto 1fr auto;
        }

        .header,
        .body,
        .actions {
          padding-inline: var(--sl-space-6, 24px);
        }
        .header { padding-block: var(--sl-space-4, 16px) var(--sl-space-3, 12px); }
        .body   { padding-block: var(--sl-space-3, 12px) var(--sl-space-4, 16px); }
        .actions{ padding-block: var(--sl-space-3, 12px) var(--sl-space-4, 16px); border-top: 1px solid var(--sl-color-line); }

        :host([padding="lg"]) .header { padding-block: var(--sl-space-6, 24px) var(--sl-space-3, 12px); }
        :host([padding="lg"]) .body   { padding-block: var(--sl-space-3, 12px) var(--sl-space-6, 24px); }
        :host([padding="lg"]) .header,
        :host([padding="lg"]) .body,
        :host([padding="lg"]) .actions {
          padding-inline: var(--sl-space-8, 32px);
        }

        :host([padding="none"]) .header,
        :host([padding="none"]) .body,
        :host([padding="none"]) .actions {
          padding: 0;
        }

        /* Hide each region cleanly when its slot has no children. */
        .header:not(:has(slot[name="header"]:not(:empty))) ,
        .actions:not(:has(slot[name="actions"]:not(:empty))) {
          /* No-op — :has on slot inside shadow DOM doesn't see slotted
             content. We use the JS hook below to toggle visibility
             instead. */
        }
        .header[hidden],
        .actions[hidden] {
          display: none;
        }

        ::slotted([slot="header"]) {
          margin: 0;
          font-size: var(--sl-font-size-md, 16px);
          font-weight: var(--sl-font-weight-semibold, 600);
          letter-spacing: var(--sl-font-letter-spacing-snug, -0.01em);
        }
        ::slotted([slot="actions"]) {
          display: flex;
          gap: var(--sl-space-2, 8px);
        }
      </style>
      <div class="surface" part="surface">
        <div class="header" part="header"><slot name="header"></slot></div>
        <div class="body" part="body"><slot></slot></div>
        <div class="actions" part="actions"><slot name="actions"></slot></div>
      </div>
    `;
    this._header = root.querySelector(".header");
    this._actions = root.querySelector(".actions");
    this._headerSlot = root.querySelector('slot[name="header"]');
    this._actionsSlot = root.querySelector('slot[name="actions"]');
  }

  connectedCallback() {
    this._headerSlot.addEventListener("slotchange", () => this._syncRegions());
    this._actionsSlot.addEventListener("slotchange", () => this._syncRegions());
    this._syncRegions();
  }

  _syncRegions() {
    // Hide regions whose slot has nothing assigned. Avoids empty padding
    // boxes when the consumer only uses the default slot.
    const headerEmpty = this._headerSlot.assignedNodes({ flatten: true }).length === 0;
    const actionsEmpty = this._actionsSlot.assignedNodes({ flatten: true }).length === 0;
    this._header.toggleAttribute("hidden", headerEmpty);
    this._actions.toggleAttribute("hidden", actionsEmpty);
  }
}

customElements.define("sl-card", SlCard);
