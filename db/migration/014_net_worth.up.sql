-- Net worth (ADR 211): assets without a market price, valued at dates, and loans valued by their schedule.
ALTER TABLE account DROP CONSTRAINT account_kind_check;
ALTER TABLE account ADD CONSTRAINT account_kind_check
    CHECK (kind IN ('current', 'savings', 'pocket', 'fund', 'broker', 'cash', 'card',
                    'p2p', 'pension', 'property', 'other_asset', 'mortgage', 'loan'));
ALTER TABLE account DROP CONSTRAINT account_institution_check;
ALTER TABLE account ADD CONSTRAINT account_institution_check
    CHECK (institution IN ('mbank_cz', 'mbank_sk', 'revolut', 'ibkr', 'fingood', 'investown', 'other'));

-- A value the user entered for an asset, or the balance owed on a loan (positive), at the end of a day.
CREATE TABLE account_valuation (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id(),
    account_id  uuid NOT NULL REFERENCES account (id) ON DELETE CASCADE,
    valued_on   date NOT NULL,
    value_minor bigint NOT NULL CHECK (value_minor >= 0),
    note        text NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, valued_on)
);

-- One loan of a P2P platform as its export showed it on the day of the snapshot; amounts are cumulative.
CREATE TABLE p2p_position (
    user_id                  uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id(),
    account_id               uuid NOT NULL REFERENCES account (id) ON DELETE CASCADE,
    snapshot_on              date NOT NULL,
    loan_number              text NOT NULL CHECK (length(loan_number) BETWEEN 1 AND 40),
    name                     text NOT NULL CHECK (length(name) <= 200),
    repayment                text NOT NULL CHECK (length(repayment) <= 40),
    status                   text NOT NULL CHECK (length(status) <= 60),
    repayment_status         text NOT NULL CHECK (length(repayment_status) <= 60),
    currency                 char(3) NOT NULL,
    invested_minor           bigint NOT NULL,
    outstanding_minor        bigint NOT NULL,
    interest_rate            numeric(8, 4) NOT NULL,
    invested_on              date,
    next_payment_on          date,
    next_payment_minor       bigint,
    final_payment_on         date,
    delay_days               int NOT NULL DEFAULT 0,
    overdue_minor            bigint NOT NULL DEFAULT 0,
    interest_paid_minor      bigint NOT NULL DEFAULT 0,
    penalty_interest_minor   bigint NOT NULL DEFAULT 0,
    principal_returned_minor bigint NOT NULL DEFAULT 0,
    market_gain_minor        bigint NOT NULL DEFAULT 0,
    fees_minor               bigint NOT NULL DEFAULT 0,
    import_batch_id          uuid NOT NULL REFERENCES import_batch (id),
    source_line              int NOT NULL,
    PRIMARY KEY (account_id, snapshot_on, loan_number)
);

CREATE INDEX p2p_position_batch ON p2p_position (import_batch_id);

-- A loan's terms from a day on: the balance owed that day, the rate and the instalment.
-- A refixation is a new row.
CREATE TABLE loan_terms (
    user_id       uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id(),
    account_id    uuid NOT NULL REFERENCES account (id) ON DELETE CASCADE,
    valid_from    date NOT NULL,
    balance_minor bigint NOT NULL CHECK (balance_minor > 0),
    annual_rate   numeric(7, 4) NOT NULL CHECK (annual_rate >= 0 AND annual_rate < 100),
    payment_minor bigint NOT NULL CHECK (payment_minor > 0),
    payment_day   int NOT NULL CHECK (payment_day BETWEEN 1 AND 31),
    maturity_on   date NOT NULL CHECK (maturity_on > valid_from),
    fixed_until   date CHECK (fixed_until > valid_from),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, valid_from)
);

-- A bank transaction that pays a loan instalment, split into interest (an expense) and
-- principal (a repayment). by_schedule: the split follows the schedule and is recomputed.
CREATE TABLE loan_payment (
    transaction_id  text PRIMARY KEY REFERENCES transaction (id) ON DELETE CASCADE,
    user_id         uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id(),
    account_id      uuid NOT NULL REFERENCES account (id) ON DELETE CASCADE,
    interest_minor  bigint NOT NULL CHECK (interest_minor >= 0),
    principal_minor bigint NOT NULL CHECK (principal_minor >= 0),
    by_schedule     boolean NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX loan_payment_account ON loan_payment (account_id);

INSERT INTO category (code, parent_id, name_cs, name_sk, name_en, kind, sort)
SELECT 'housing.mortgage_interest', p.id, 'Úroky z hypotéky', 'Úroky z hypotéky', 'Mortgage interest', p.kind, 80
FROM category p WHERE p.code = 'housing';

-- Foreign keys bypass row-level security, so writes also check that the account (and the
-- transaction) is visible to the user, not only that the row carries their id.
ALTER TABLE account_valuation ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_valuation FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON account_valuation USING (user_id = current_user_id())
    WITH CHECK (user_id = current_user_id() AND EXISTS (SELECT 1 FROM account a WHERE a.id = account_valuation.account_id));
ALTER TABLE p2p_position ENABLE ROW LEVEL SECURITY;
ALTER TABLE p2p_position FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON p2p_position USING (user_id = current_user_id())
    WITH CHECK (user_id = current_user_id() AND EXISTS (SELECT 1 FROM account a WHERE a.id = p2p_position.account_id));
ALTER TABLE loan_terms ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_terms FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON loan_terms USING (user_id = current_user_id())
    WITH CHECK (user_id = current_user_id() AND EXISTS (SELECT 1 FROM account a WHERE a.id = loan_terms.account_id));
ALTER TABLE loan_payment ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_payment FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON loan_payment USING (user_id = current_user_id())
    WITH CHECK (user_id = current_user_id() AND EXISTS (SELECT 1 FROM account a WHERE a.id = loan_payment.account_id)
        AND EXISTS (SELECT 1 FROM transaction t WHERE t.id = loan_payment.transaction_id));
