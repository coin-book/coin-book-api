DROP POLICY own_rows ON investment_tx;
CREATE POLICY own_rows ON investment_tx USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
DELETE FROM investment_tx WHERE import_batch_id IS NULL;
ALTER TABLE investment_tx DROP CONSTRAINT investment_tx_imported_or_manual;
ALTER TABLE investment_tx ALTER COLUMN source_line SET NOT NULL;
ALTER TABLE investment_tx ALTER COLUMN import_batch_id SET NOT NULL;
