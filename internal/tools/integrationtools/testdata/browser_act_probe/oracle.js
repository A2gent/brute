/* Trusted, offline fixtures only. This oracle is NEVER included in the Jev request.
   Labels are manually authored next-action/outcome truth, not confidence-derived truth. */
window.probe = {
  choice(operation, label = '') { return {operation, label}; },
  truth(state, expected, success = false, evidence = '') { return {state, expected, success, evidence}; },
  visible(id) { const e = document.getElementById(id), r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.y + r.height / 2 >= 0 && r.y + r.height / 2 < innerHeight; }
};
