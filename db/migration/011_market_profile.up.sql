-- What Yahoo's chart answer says about a symbol beyond its bars (ADR 209), refreshed with the prices.
ALTER TABLE price_series
    ADD COLUMN long_name             text,
    ADD COLUMN exchange              text,
    ADD COLUMN instrument_type       text,
    ADD COLUMN exchange_timezone     text,
    ADD COLUMN first_trade_on        date,
    ADD COLUMN fifty_two_week_low    numeric(24, 8),
    ADD COLUMN fifty_two_week_high   numeric(24, 8),
    ADD COLUMN day_low               numeric(24, 8),
    ADD COLUMN day_high              numeric(24, 8),
    ADD COLUMN day_volume            bigint,
    ADD COLUMN market_price          numeric(24, 8),
    ADD COLUMN market_change_percent numeric(12, 6),
    ADD COLUMN market_time           timestamptz;
