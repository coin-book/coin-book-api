-- A movement to or from an own account whose statement is not imported: one leg, no pair yet (ADR 206).
CREATE TABLE own_transfer (
    transaction_id text PRIMARY KEY REFERENCES transaction (id) ON DELETE CASCADE,
    account_id     uuid NOT NULL REFERENCES account (id) ON DELETE CASCADE,
    matched_by     text NOT NULL CHECK (matched_by IN ('number', 'counterparty', 'manual')),
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX own_transfer_account ON own_transfer (account_id);

-- Counterparties the user marked as an own account, for payments that carry no account number.
CREATE TABLE own_transfer_rule (
    counterparty text PRIMARY KEY CHECK (counterparty = lower(counterparty)),
    account_id   uuid NOT NULL REFERENCES account (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- The user said this is not a transfer; detection by account number must not bring it back.
CREATE TABLE not_transfer (
    transaction_id text PRIMARY KEY REFERENCES transaction (id) ON DELETE CASCADE,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- Every transaction that moves money between own accounts, one row per leg.
CREATE VIEW transfer_leg AS
SELECT tp.out_tx_id AS transaction_id, tp.in_tx_id AS counterpart_tx_id, i.account_id AS counterpart_account_id, tp.matched_by
FROM transfer_pair tp JOIN transaction i ON i.id = tp.in_tx_id
UNION ALL
SELECT tp.in_tx_id, tp.out_tx_id, o.account_id, tp.matched_by
FROM transfer_pair tp JOIN transaction o ON o.id = tp.out_tx_id
UNION ALL
SELECT ot.transaction_id, NULL, ot.account_id, ot.matched_by FROM own_transfer ot;
