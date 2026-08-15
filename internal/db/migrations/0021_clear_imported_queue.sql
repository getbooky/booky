-- Imported grabs no longer linger in the queue (MarkDone deletes the row, and
-- history keeps the "imported" entry). Clear the ones an older build left
-- behind, so an install that has been running a while opens Activity on the
-- handful of grabs still in flight instead of a wall of finished ones.
DELETE FROM queue WHERE status = 'done';
