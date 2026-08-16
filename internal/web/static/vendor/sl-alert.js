// <sl-alert variant="error|notice|warn">message</sl-alert>
//
// Page-level flash banner. Variants map to Stormlantern semantic colours
// (loss / gain / amber-warn). Sets `role` to "alert" (assertive — error
// context, screen reader interrupts) for error/warn, and "status" (polite)
// for notice. Default variant is "error" so an unclassified server-emitted
// banner still gets announced.

class SlAlert extends HTMLElement {
  static observedAttributes = ["variant"];

  constructor() {
    super();
    const root = this.attachShadow({ mode: "open" });
    root.innerHTML = `
      <style>
        :host {
          display: block;
          margin-bottom: var(--sl-space-4, 16px);
        }
        :host([hidden]) { display: none; }
        .surface {
          border-radius: var(--sl-radius-md, 4px);
          padding: var(--sl-space-3, 12px) var(--sl-space-4, 16px);
          font-family: var(--sl-font-family-body, ui-sans-serif, system-ui, sans-serif);
          font-size: var(--sl-font-size-base, 14px);
          background: var(--sl-color-loss-tint);
          color: var(--sl-color-loss);
          border: 1px solid var(--sl-color-loss-tint);
        }
        :host([variant="notice"]) .surface {
          background: var(--sl-color-gain-tint);
          color: var(--sl-color-gain);
          border-color: var(--sl-color-gain-tint);
        }
        :host([variant="warn"]) .surface {
          background: var(--sl-color-warn-tint);
          color: var(--sl-color-warn);
          border-color: var(--sl-color-warn-tint);
        }
      </style>
      <div class="surface" part="surface"><slot></slot></div>
    `;
  }

  connectedCallback() {
    this._syncRole();
  }

  attributeChangedCallback() {
    this._syncRole();
  }

  _syncRole() {
    // notice = polite ("status"), error/warn = assertive ("alert").
    const role = this.getAttribute("variant") === "notice" ? "status" : "alert";
    this.setAttribute("role", role);
  }
}

customElements.define("sl-alert", SlAlert);
