-- What a model phase carries from one night to the next (lina#444). A phase whose result is built from several
-- answers (the concepts phase groups claims batch by batch) keeps each answered subject's share here, so a night
-- that stops at its budget between batches does not lose the groups of the batches it already answered.
-- NULL for a phase whose answers are used up the moment they are written. Not selected by agents beyond the
-- grant on the table (0019).

ALTER TABLE brain.dream_inputs ADD COLUMN IF NOT EXISTS state jsonb;
