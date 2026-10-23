// SPDX-License-Identifier: Apache-2.0
package canon

const conversationQuestionsSchema = `
CREATE TABLE conversation_questions (
 id TEXT PRIMARY KEY, person TEXT NOT NULL, agent TEXT NOT NULL,
 turn TEXT NOT NULL, record BLOB NOT NULL
);
CREATE INDEX conversation_questions_turn ON conversation_questions(person,agent,turn);
CREATE TABLE conversation_question_resolutions (
 question TEXT PRIMARY KEY REFERENCES conversation_questions(id), record BLOB NOT NULL
);
`
