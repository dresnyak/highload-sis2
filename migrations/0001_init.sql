-- +goose Up
CREATE TABLE tasks (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author_id         bigint       NOT NULL,
    group_id          bigint,
    title             varchar(500) NOT NULL CHECK (length(title) > 0),
    description       text         NOT NULL DEFAULT '',
    status            varchar(16)  NOT NULL DEFAULT 'planned'
                      CHECK (status IN ('planned', 'in_progress', 'completed')),
    priority          smallint     NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 3),
    client_request_id uuid         NOT NULL,
    request_hash      bytea        NOT NULL,
    version           bigint       NOT NULL DEFAULT 1,
    created_at        timestamptz  NOT NULL DEFAULT now(),
    updated_at        timestamptz  NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);

CREATE UNIQUE INDEX uq_tasks_idem ON tasks (author_id, client_request_id);

CREATE INDEX idx_tasks_personal_priority ON tasks (author_id, priority DESC, id DESC)
    WHERE group_id IS NULL AND deleted_at IS NULL;

CREATE INDEX idx_tasks_group_priority ON tasks (group_id, priority DESC, id DESC)
    WHERE group_id IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX idx_tasks_personal_created ON tasks (author_id, created_at DESC, id DESC)
    WHERE group_id IS NULL AND deleted_at IS NULL;

CREATE INDEX idx_tasks_group_created ON tasks (group_id, created_at DESC, id DESC)
    WHERE group_id IS NOT NULL AND deleted_at IS NULL;

-- +goose Down
DROP TABLE tasks;
