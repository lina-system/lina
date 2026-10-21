// SPDX-License-Identifier: Apache-2.0
package canon

// Account sign-in metadata is shared by Core, backup and read-only diagnosis.
const AccountOwnerBindingSchema = `CREATE TABLE owner_binding (
	singleton INTEGER PRIMARY KEY CHECK (singleton=1),
	person TEXT NOT NULL CHECK (length(person)>0),
	issuer TEXT NOT NULL CHECK (length(issuer)>0),
	subject TEXT NOT NULL CHECK (length(subject)>0)
);`

const AccountSignInSchema = AccountOwnerBindingSchema + `
CREATE TABLE sign_in_host (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 host_id TEXT NOT NULL UNIQUE
);
CREATE TABLE sign_in_identities (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id),
 issuer TEXT NOT NULL,
 subject TEXT NOT NULL
);
`
