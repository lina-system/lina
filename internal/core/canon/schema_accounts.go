// SPDX-License-Identifier: Apache-2.0
package canon

// Account metadata contains no tokens. A pending exchange is durably marked
// needs_sign_in before contacting the issuer, so interruption never replays an
// already consumed refresh token. Tokens live only in the secret store.
const accountsSchema = `
CREATE TABLE accounts (
 id TEXT PRIMARY KEY,
 record BLOB NOT NULL CHECK(json_valid(record))
);
`
