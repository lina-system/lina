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
      || scope.selectedComponents.some(name => !['kit', 'lina', 'moirai-worker', 'lina-node', 'crw-takeover'].includes(name))
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
    // Older evidence has only the component aggregate. A sharded selection
    // must account for each exact shard; an aggregate cannot conceal omissions.
    if (scope.componentShards !== undefined) {
      const selected = scope.componentShards;
      const shards = results.components?.shards;
      if (!Array.isArray(selected) || scope.components !== (selected.length > 0)
          || selected.some(id => typeof id !== 'string' || !/^(build|unit|e2e)(?:-[a-z0-9]+)*$/.test(id))
          || new Set(selected).size !== selected.length) {
        failures.push('component shard selection is malformed');
      } else if (!shards || typeof shards !== 'object' || Array.isArray(shards)) {
        failures.push('component shard results are missing or malformed');
      } else {
        for (const id of selected) {
          if (!Object.hasOwn(shards, id) || shards[id]?.result !== 'success') {
            failures.push(`${id}: expected success, received ${shards[id]?.result ?? 'missing'}`);
          }
        }
        for (const id of Object.keys(shards)) {
          if (!selected.includes(id)) failures.push(`unexpected component shard: ${id}`);
        }
      }
    }
  }
  return { result: failures.length ? 'failure' : 'success', mode: scope.mode, head: scope.head, failures };
}
