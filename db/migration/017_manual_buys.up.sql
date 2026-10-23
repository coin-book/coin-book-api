-- A purchase the user entered has no import: brokers' early statements may leave it out.
ALTER TABLE investment_tx ALTER COLUMN import_batch_id DROP NOT NULL;
ALTER TABLE investment_tx ALTER COLUMN source_line DROP NOT NULL;
ALTER TABLE investment_tx ADD CONSTRAINT investment_tx_imported_or_manual
    CHECK ((import_batch_id IS NULL) = (source_line IS NULL) AND (import_batch_id IS NOT NULL OR kind = 'buy'));

-- The account comes from the request now, and foreign keys bypass row-level security.
DROP POLICY own_rows ON investment_tx;
CREATE POLICY own_rows ON investment_tx USING (user_id = current_user_id())
    WITH CHECK (user_id = current_user_id() AND EXISTS (SELECT 1 FROM account a WHERE a.id = investment_tx.account_id));
