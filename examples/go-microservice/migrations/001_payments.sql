-- The schema analyzer will link these objects to the Go types and queries that
-- touch them, so `le graph impact payments --change schema` reports the code
-- that needs a migration alongside it.
CREATE TABLE payments (
    id         TEXT PRIMARY KEY,
    amount     BIGINT      NOT NULL CHECK (amount > 0),
    currency   CHAR(3)     NOT NULL,
    status     TEXT        NOT NULL CHECK (status IN ('pending', 'settled', 'refunded', 'failed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX payments_by_status ON payments (status, created_at DESC);
