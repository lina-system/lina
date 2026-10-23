// SPDX-License-Identifier: Apache-2.0
package canon

const workReviewsSchema = `
CREATE TABLE work_reviews (
 repository TEXT NOT NULL,
 pr_number INTEGER NOT NULL,
 head TEXT NOT NULL,
 record BLOB NOT NULL,
 PRIMARY KEY(repository,pr_number,head)
) WITHOUT ROWID;
`
