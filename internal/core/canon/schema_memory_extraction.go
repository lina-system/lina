// SPDX-License-Identifier: Apache-2.0
package canon

const MemoryExtractionSchema = `
CREATE TABLE memory_extraction_cursor (
 job TEXT PRIMARY KEY, through INTEGER NOT NULL CHECK(through>=0)
);
`
