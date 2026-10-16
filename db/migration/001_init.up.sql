CREATE TABLE app_user (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE account (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                  text NOT NULL,
    institution           text NOT NULL CHECK (institution IN ('mbank_cz', 'mbank_sk', 'revolut', 'ibkr', 'other')),
    kind                  text NOT NULL CHECK (kind IN ('current', 'savings', 'pocket', 'fund', 'broker')),
    currency              char(3) NOT NULL,
    -- Bank account number "prefix-number/bank" or IBAN; NULL for accounts a bank does not number (Revolut pockets).
    account_number        text UNIQUE,
    -- Stable identity the importer uses when there is no number, e.g. "revolut:personal:EUR".
    external_ref          text UNIQUE,
    parent_id             uuid REFERENCES account (id),
    opening_balance_minor bigint NOT NULL DEFAULT 0,
    color_key             text,
    archived              boolean NOT NULL DEFAULT false,
    created_at            timestamptz NOT NULL DEFAULT now(),
    CHECK (account_number IS NOT NULL OR external_ref IS NOT NULL)
);

CREATE TABLE import_batch (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    file_name      text NOT NULL,
    file_hash      text NOT NULL,
    format         text NOT NULL,
    parser_version int NOT NULL,
    period_from    date,
    period_to      date,
    status         text NOT NULL CHECK (status IN ('committed', 'rolled_back')),
    forced         boolean NOT NULL DEFAULT false,
    rows_total     int NOT NULL,
    rows_inserted  int NOT NULL,
    rows_duplicate int NOT NULL,
    check_report   jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at     timestamptz NOT NULL DEFAULT now(),
    rolled_back_at timestamptz
);

-- A rolled-back file may be imported again, a committed one only once.
CREATE UNIQUE INDEX import_batch_committed_file ON import_batch (file_hash) WHERE status = 'committed';

CREATE TABLE transaction (
    id                  text PRIMARY KEY,
    account_id          uuid NOT NULL REFERENCES account (id),
    booked_on           date NOT NULL,
    amount_minor        bigint NOT NULL,
    currency            char(3) NOT NULL,
    fee_minor           bigint NOT NULL DEFAULT 0,
    description         text NOT NULL,
    counterparty        text,
    counterparty_account text,
    operation_type      text,
    message             text,
    variable_symbol     text,
    constant_symbol     text,
    specific_symbol     text,
    bank_category       text,
    balance_after_minor bigint,
    import_batch_id     uuid NOT NULL REFERENCES import_batch (id),
    source_line         int NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX transaction_account_booked ON transaction (account_id, booked_on);
CREATE INDEX transaction_booked ON transaction (booked_on);
CREATE INDEX transaction_batch ON transaction (import_batch_id);
CREATE INDEX transaction_currency_amount ON transaction (currency, amount_minor);
CREATE INDEX transaction_counterparty_account ON transaction (counterparty_account) WHERE counterparty_account IS NOT NULL;

CREATE TABLE budget_group (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code    text NOT NULL UNIQUE,
    name_cs text NOT NULL,
    name_sk text NOT NULL,
    sort    int NOT NULL DEFAULT 0
);

CREATE TABLE category (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code            text NOT NULL UNIQUE,
    parent_id       uuid REFERENCES category (id),
    name_cs         text NOT NULL,
    name_sk         text NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('income', 'expense', 'financial', 'transfer')),
    -- Token key from packages/ui theme.css, never a hex value; NULL on a subcategory, which inherits its parent's.
    color_key       text,
    icon            text,
    budget_group_id uuid REFERENCES budget_group (id),
    sort            int NOT NULL DEFAULT 0
);

CREATE TABLE category_assignment (
    transaction_id text NOT NULL REFERENCES transaction (id) ON DELETE CASCADE,
    category_id    uuid NOT NULL REFERENCES category (id),
    amount_minor   bigint NOT NULL,
    source         text NOT NULL CHECK (source IN ('rule', 'manual', 'bank')),
    rule_id        uuid,
    assigned_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (transaction_id, category_id)
);

CREATE INDEX category_assignment_category ON category_assignment (category_id);

CREATE TABLE rule (
    id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    category_id                uuid NOT NULL REFERENCES category (id),
    priority                   int NOT NULL DEFAULT 100,
    match_counterparty         text,
    match_description          text,
    match_counterparty_account text,
    match_operation_type       text,
    match_variable_symbol      text,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    CHECK (num_nonnulls(match_counterparty, match_description, match_counterparty_account,
                        match_operation_type, match_variable_symbol) > 0)
);

ALTER TABLE category_assignment
    ADD CONSTRAINT category_assignment_rule FOREIGN KEY (rule_id) REFERENCES rule (id) ON DELETE SET NULL;

-- A transfer is recognised by the existence of its pair, never by a flag on the transaction.
CREATE TABLE transfer_pair (
    out_tx_id  text NOT NULL UNIQUE REFERENCES transaction (id) ON DELETE CASCADE,
    in_tx_id   text NOT NULL UNIQUE REFERENCES transaction (id) ON DELETE CASCADE,
    matched_by text NOT NULL CHECK (matched_by IN ('auto', 'manual')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (out_tx_id, in_tx_id),
    CHECK (out_tx_id <> in_tx_id)
);

CREATE TABLE budget (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    month           date NOT NULL CHECK (extract(day FROM month) = 1),
    category_id     uuid REFERENCES category (id),
    budget_group_id uuid REFERENCES budget_group (id),
    amount_minor    bigint NOT NULL CHECK (amount_minor >= 0),
    currency        char(3) NOT NULL,
    CHECK (num_nonnulls(category_id, budget_group_id) = 1)
);

CREATE UNIQUE INDEX budget_month_category ON budget (month, category_id) WHERE category_id IS NOT NULL;
CREATE UNIQUE INDEX budget_month_group ON budget (month, budget_group_id) WHERE budget_group_id IS NOT NULL;

CREATE TABLE agent_token (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    token_hash   text NOT NULL UNIQUE,
    last4        text NOT NULL,
    scopes       text[] NOT NULL,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id           bigserial PRIMARY KEY,
    token_id     uuid NOT NULL REFERENCES agent_token (id),
    operation    text NOT NULL,
    status       int NOT NULL,
    result_count int,
    occurred_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_occurred ON audit_log (occurred_at DESC);

CREATE RULE audit_log_no_update AS ON UPDATE TO audit_log DO INSTEAD NOTHING;
CREATE RULE audit_log_no_delete AS ON DELETE TO audit_log DO INSTEAD NOTHING;
