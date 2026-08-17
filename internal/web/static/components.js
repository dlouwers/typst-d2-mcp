// Registers the Stormlantern custom elements used by the admin UI.
//
// These are display-only wrappers (table chrome, cards, badges, flash
// alerts). If this module fails to load, the browser treats sl-* as
// unknown elements and renders their contents unstyled — the page stays
// readable and every form still works, because the controls inside them
// are native <input> and <button>.
import "./vendor/sl-table.js";
import "./vendor/sl-card.js";
import "./vendor/sl-badge.js";
import "./vendor/sl-alert.js";
