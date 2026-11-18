-- An agent's answer to a colleague who is still running (#38).
--
-- covey/add_note with "answer" resumes a task parked on a correlation key
-- under "answer:". The asker delegates with covey/create_task and parks when
-- its run ends — but create_task wakes the colleague at once, so the answer
-- can come while the asker still runs, and a park after it would wait for an
-- answer that has already come. Such an answer is held here for the run it
-- arrived in, and that run's park takes it: a task that parks on an "answer:"
-- key with an answer held for the run that parks resumes at once
-- (backlog.Store.Block).
--
-- run is the task's edge into in_progress that started the run the answer
-- was held for. An answer held for an earlier run — one that ended without
-- parking for it — is never taken by a later one.
CREATE TABLE task_held_answers (
    id         BIGSERIAL PRIMARY KEY,
    task_id    UUID NOT NULL REFERENCES backlog_tasks(id) ON DELETE CASCADE,
    run        BIGINT NOT NULL,
    author     TEXT NOT NULL,
    input      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_task_held_answers_task ON task_held_answers (task_id);
