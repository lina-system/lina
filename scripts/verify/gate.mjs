// SPDX-License-Identifier: Apache-2.0
// The gate: foundation passes only when selection succeeded, every selected check succeeded, and
// every unselected check was skipped. Missing, malformed, or unexpected results fail it.

export function evaluateGate(scope, results) {
  const failures = [];
  if (results.selection?.result !== 'success') {
    failures.push('selection did not succeed');
  } else if (scope.docs !== true || typeof scope.automation !== 'boolean' || !Array.isArray(scope.unmapped)
      || typeof scope.dependencies !== 'boolean'
      || typeof scope.components !== 'boolean' || !Array.isArray(scope.selectedComponents)
      || scope.components !== (scope.selectedComponents.length > 0)
      || scope.selectedComponents.some(name => !['crw-takeover', 'kit', 'lina', 'moirai-worker', 'lina-node'].includes(name))
      || new Set(scope.selectedComponents).size !== scope.selectedComponents.length) {
    failures.push('selection output is missing or malformed');
  } else {
    if (scope.unmapped.length) {
      failures.push(`Register verification for unmapped paths: ${scope.unmapped.join(', ')}`);
    }
    for (const name of ['docs', 'automation', 'dependencies', 'components']) {
      const expected = scope[name] ? 'success' : 'skipped';
      const actual = results[name]?.result;
      if (actual !== expected) failures.push(`${name}: expected ${expected}, received ${actual ?? 'missing'}`);
    }
  }
  return { result: failures.length ? 'failure' : 'success', mode: scope.mode, head: scope.head, failures };
}
