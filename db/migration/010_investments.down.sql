DROP INDEX import_batch_committed_file;
-- A file imported again by a newer parser version has two committed batches; the older one
-- stops counting as committed so the old index fits. Its transactions stay.
UPDATE import_batch b SET status = 'rolled_back', rolled_back_at = now()
WHERE status = 'committed' AND EXISTS (SELECT 1 FROM import_batch n WHERE n.user_id = b.user_id
    AND n.file_hash = b.file_hash AND n.status = 'committed' AND n.parser_version > b.parser_version);
CREATE UNIQUE INDEX import_batch_committed_file ON import_batch (user_id, file_hash) WHERE status = 'committed';
DROP TABLE instrument_symbol;
DROP TABLE investment_lot_sale;
DROP TABLE investment_tx;
DROP TABLE fx_rate;
DROP TABLE price_bar;
DROP TABLE price_series;
DROP TABLE instrument;
