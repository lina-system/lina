// SPDX-License-Identifier: Apache-2.0
'use strict';
document.addEventListener('click', event => {
  const control = event.target.closest('button[data-action="toggle"]');
  if (!control) return;
  const target = document.getElementById(control.dataset.target);
  if (target) {
    target.hidden = !target.hidden;
    control.setAttribute('aria-expanded', String(!target.hidden));
  }
});
window.addEventListener('error', () => { document.documentElement.dataset.linaError = 'true'; });
window.addEventListener('unhandledrejection', () => { document.documentElement.dataset.linaError = 'true'; });
document.documentElement.dataset.linaReady = 'true';
