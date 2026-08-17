// <sl-badge variant="ok|warn|info|neutral">label</sl-badge>
//
// Visual-only pill atom. Light DOM is the slot; styles live in Shadow DOM
// so host CSS doesn't leak in. Tokens pierce the shadow boundary, so the
// badge tracks light/dark theme automatically as long as
// @dlouwers/stormlantern-tokens is loaded on the host page.
//
// `variant` defaults to "ok". `setAttribute('variant', '...')` is the live
// mutation API.

class SlBadge extends HTMLElement {
  static observedAttributes = ["variant"];

  constructor() {
    super();
    const root = this.attachShadow({ mode: "open" });
    root.innerHTML = `
      <style>
        :host {
          display: inline-block;
        }
        :host([hidden]) { display: none; }
        .pill {
          display: inline-block;
          padding: 2px 9px;
          border-radius: var(--sl-radius-pill, 999px);
          font-family: var(--sl-font-family-body, ui-sans-serif, system-ui, sans-serif);
          font-size: var(--sl-font-size-xs, 11px);
          font-weight: var(--sl-font-weight-semibold, 600);
          letter-spacing: 0.02em;
          text-transform: uppercase;
          background: var(--sl-color-gain-tint);
          color: var(--sl-color-gain);
        }
        :host([variant="warn"]) .pill {
          background: var(--sl-color-warn-tint);
          color: var(--sl-color-warn);
        }
        :host([variant="info"]) .pill {
          background: var(--sl-color-accent-tint);
          color: var(--sl-color-accent-hover);
        }
        :host([variant="neutral"]) .pill {
          background: var(--sl-color-gray-100);
          color: var(--sl-color-muted);
        }
      </style>
      <span class="pill" part="pill"><slot></slot></span>
    `;
  }
}

customElements.define("sl-badge", SlBadge);
