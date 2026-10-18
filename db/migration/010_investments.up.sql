-- Investments (ADR 209). Instruments, prices and FX rates are public market data shared by all
-- users, without user_id or a policy; what a user holds, and the symbol they price it by, is theirs.
-- Nothing shared records who holds what or since when.
CREATE TABLE instrument (
    isin         text PRIMARY KEY CHECK (isin ~ '^[A-Z]{2}[A-Z0-9]{9}[0-9]$'),
    ticker       text CHECK (length(ticker) <= 12),
    name         text NOT NULL DEFAULT '' CHECK (length(name) <= 120),
    -- The symbol guessed at the first import; a user's own choice is in instrument_symbol.
    yahoo_symbol text CHECK (length(yahoo_symbol) <= 20),
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- A Yahoo symbol's quote currency and how far its daily bars are fetched; currency is NULL
-- for a symbol Yahoo returned nothing for.
CREATE TABLE price_series (
    symbol          text PRIMARY KEY,
    currency        char(3),
    fetched_through date NOT NULL
);

CREATE TABLE price_bar (
    symbol text NOT NULL,
    day    date NOT NULL,
    open   numeric(24, 8),
    high   numeric(24, 8),
    low    numeric(24, 8),
    close  numeric(24, 8) NOT NULL,
    volume bigint,
    source text NOT NULL,
    PRIMARY KEY (symbol, day)
);

-- ČNB daily rate: CZK for one unit of the currency.
CREATE TABLE fx_rate (
    day          date NOT NULL,
    currency     char(3) NOT NULL,
    czk_per_unit numeric(24, 10) NOT NULL CHECK (czk_per_unit > 0),
    source       text NOT NULL,
    PRIMARY KEY (currency, day)
);

-- Buys and dividends. Fees and taxes are kept in CZK only: the statement may charge them
-- in another currency than the trade's, and its CZK column is the one that is consistent.
CREATE TABLE investment_tx (
    id              text PRIMARY KEY,
    user_id         uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id(),
    account_id      uuid NOT NULL REFERENCES account (id),
    isin            text NOT NULL REFERENCES instrument (isin),
    kind            text NOT NULL CHECK (kind IN ('buy', 'dividend')),
    traded_on       date NOT NULL,
    quantity        numeric(28, 10) CHECK (quantity > 0),
    currency        char(3) NOT NULL,
    gross_minor     bigint NOT NULL,
    net_minor       bigint NOT NULL,
    gross_czk_minor bigint,
    net_czk_minor   bigint,
    fee_czk_minor   bigint NOT NULL DEFAULT 0,
    tax_czk_minor   bigint NOT NULL DEFAULT 0,
    import_batch_id uuid NOT NULL REFERENCES import_batch (id),
    source_line     int NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'buy') = (quantity IS NOT NULL))
);

CREATE INDEX investment_tx_user_isin ON investment_tx (user_id, isin, traded_on);
CREATE INDEX investment_tx_batch ON investment_tx (import_batch_id);
CREATE INDEX investment_tx_account ON investment_tx (account_id);

-- A sold lot as the broker matched it: when it was bought, when sold, and the gain.
CREATE TABLE investment_lot_sale (
    id                 text PRIMARY KEY,
    user_id            uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id(),
    account_id         uuid NOT NULL REFERENCES account (id),
    isin               text NOT NULL REFERENCES instrument (isin),
    bought_on          date NOT NULL,
    sold_on            date NOT NULL CHECK (sold_on >= bought_on),
    quantity           numeric(28, 10) NOT NULL CHECK (quantity > 0),
    currency           char(3) NOT NULL,
    proceeds_minor     bigint NOT NULL,
    cost_minor         bigint NOT NULL,
    gain_minor         bigint NOT NULL,
    proceeds_czk_minor bigint,
    cost_czk_minor     bigint,
    gain_czk_minor     bigint,
    fee_czk_minor      bigint NOT NULL DEFAULT 0,
    tax_czk_minor      bigint NOT NULL DEFAULT 0,
    import_batch_id    uuid NOT NULL REFERENCES import_batch (id),
    source_line        int NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX investment_lot_sale_user_isin ON investment_lot_sale (user_id, isin, sold_on);
CREATE INDEX investment_lot_sale_batch ON investment_lot_sale (import_batch_id);
CREATE INDEX investment_lot_sale_account ON investment_lot_sale (account_id);

-- The user's own Yahoo symbol for an instrument; NULL: price it at cost.
CREATE TABLE instrument_symbol (
    user_id      uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id(),
    isin         text NOT NULL REFERENCES instrument (isin),
    yahoo_symbol text CHECK (length(yahoo_symbol) <= 20),
    PRIMARY KEY (user_id, isin)
);

ALTER TABLE instrument_symbol ENABLE ROW LEVEL SECURITY;
ALTER TABLE instrument_symbol FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON instrument_symbol USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE investment_tx ENABLE ROW LEVEL SECURITY;
ALTER TABLE investment_tx FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON investment_tx USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE investment_lot_sale ENABLE ROW LEVEL SECURITY;
ALTER TABLE investment_lot_sale FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON investment_lot_sale USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());

-- A file read by an older parser version can be imported again for what that version skipped.
DROP INDEX import_batch_committed_file;
CREATE UNIQUE INDEX import_batch_committed_file ON import_batch (user_id, file_hash, parser_version) WHERE status = 'committed';
