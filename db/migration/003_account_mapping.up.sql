-- Remembers which own account a statement account belongs to when the user mapped it
-- by hand, so the next import of the same bank account suggests it again.
CREATE TABLE account_alias (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    institution text NOT NULL,
    identity    text NOT NULL,
    account_id  uuid NOT NULL REFERENCES account (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (institution, identity)
);

ALTER TABLE account DROP CONSTRAINT account_kind_check;
ALTER TABLE account ADD CONSTRAINT account_kind_check
    CHECK (kind IN ('current', 'savings', 'pocket', 'fund', 'broker', 'cash', 'card'));
