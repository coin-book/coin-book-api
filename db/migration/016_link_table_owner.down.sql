DROP INDEX audit_log_user;
DROP INDEX category_assignment_rule;
CREATE INDEX account_user ON account (user_id);
CREATE INDEX transaction_booked ON transaction (booked_on);
DROP INDEX transaction_user_amount;
CREATE INDEX transaction_currency_amount ON transaction (currency, amount_minor);

DROP POLICY own_rows ON not_transfer;
CREATE POLICY own_rows ON not_transfer
    USING (EXISTS (SELECT 1 FROM transaction t WHERE t.id = not_transfer.transaction_id))
    WITH CHECK (EXISTS (SELECT 1 FROM transaction t WHERE t.id = not_transfer.transaction_id));
DROP POLICY own_rows ON own_transfer;
CREATE POLICY own_rows ON own_transfer
    USING (EXISTS (SELECT 1 FROM transaction t WHERE t.id = own_transfer.transaction_id))
    WITH CHECK (EXISTS (SELECT 1 FROM transaction t WHERE t.id = own_transfer.transaction_id));
DROP POLICY own_rows ON transfer_pair;
CREATE POLICY own_rows ON transfer_pair
    USING (EXISTS (SELECT 1 FROM transaction t WHERE t.id = transfer_pair.out_tx_id))
    WITH CHECK (EXISTS (SELECT 1 FROM transaction t WHERE t.id = transfer_pair.out_tx_id)
        AND EXISTS (SELECT 1 FROM transaction t WHERE t.id = transfer_pair.in_tx_id));
DROP POLICY own_rows ON category_assignment;
CREATE POLICY own_rows ON category_assignment
    USING (EXISTS (SELECT 1 FROM transaction t WHERE t.id = category_assignment.transaction_id))
    WITH CHECK (EXISTS (SELECT 1 FROM transaction t WHERE t.id = category_assignment.transaction_id));

ALTER TABLE not_transfer DROP COLUMN user_id;
ALTER TABLE own_transfer DROP COLUMN user_id;
ALTER TABLE transfer_pair DROP COLUMN user_id;
ALTER TABLE category_assignment DROP COLUMN user_id;
