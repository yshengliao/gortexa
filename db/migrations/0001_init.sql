-- Resources table. Type alignment (B-6): proto string ↔ text, enum ↔ text
-- (enum rendered as string everywhere), Timestamp ↔ timestamptz.
CREATE TABLE resources (
    id         text        PRIMARY KEY,
    name       text        NOT NULL,
    owner      text        NOT NULL,
    status     text        NOT NULL DEFAULT 'STATUS_ACTIVE',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_resources_owner ON resources (owner);
