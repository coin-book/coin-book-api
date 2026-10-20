-- Link tables carry their owner: policies compare a column instead of a per-row lookup (ADR 208).
-- WITH CHECK keeps the lookup, foreign keys bypass row-level security. No foreign key to app_user:
-- rows go with their transaction, and its check would run for every row an import writes.
ALTER TABLE category_assignment ADD COLUMN user_id uuid DEFAULT current_user_id();
ALTER TABLE transfer_pair ADD COLUMN user_id uuid DEFAULT current_user_id();
ALTER TABLE own_transfer ADD COLUMN user_id uuid DEFAULT current_user_id();
ALTER TABLE not_transfer ADD COLUMN user_id uuid DEFAULT current_user_id();

-- A schema owner that is not a superuser is bound by FORCE and would see no rows to backfill.
ALTER TABLE category_assignment NO FORCE ROW LEVEL SECURITY;
ALTER TABLE transfer_pair NO FORCE ROW LEVEL SECURITY;
ALTER TABLE own_transfer NO FORCE ROW LEVEL SECURITY;
ALTER TABLE not_transfer NO FORCE ROW LEVEL SECURITY;
UPDATE category_assignment x SET user_id = t.user_id FROM transaction t WHERE t.id = x.transaction_id;
UPDATE transfer_pair x SET user_id = t.user_id FROM transaction t WHERE t.id = x.out_tx_id;
UPDATE own_transfer x SET user_id = t.user_id FROM transaction t WHERE t.id = x.transaction_id;
UPDATE not_transfer x SET user_id = t.user_id FROM transaction t WHERE t.id = x.transaction_id;
ALTER TABLE category_assignment FORCE ROW LEVEL SECURITY;
ALTER TABLE transfer_pair FORCE ROW LEVEL SECURITY;
ALTER TABLE own_transfer FORCE ROW LEVEL SECURITY;
ALTER TABLE not_transfer FORCE ROW LEVEL SECURITY;

ALTER TABLE category_assignment ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE transfer_pair ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE own_transfer ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE not_transfer ALTER COLUMN user_id SET NOT NULL;

DROP POLICY own_rows ON category_assignment;
CREATE POLICY own_rows ON category_assignment USING (user_id = current_user_id())
    WITH CHECK (user_id = current_user_id() AND EXISTS (SELECT 1 FROM transaction t WHERE t.id = category_assignment.transaction_id));
DROP POLICY own_rows ON transfer_pair;
CREATE POLICY own_rows ON transfer_pair USING (user_id = current_user_id())
    WITH CHECK (user_id = current_user_id() AND EXISTS (SELECT 1 FROM transaction t WHERE t.id = transfer_pair.out_tx_id)
        AND EXISTS (SELECT 1 FROM transaction t WHERE t.id = transfer_pair.in_tx_id));
DROP POLICY own_rows ON own_transfer;
CREATE POLICY own_rows ON own_transfer USING (user_id = current_user_id())
    WITH CHECK (user_id = current_user_id() AND EXISTS (SELECT 1 FROM transaction t WHERE t.id = own_transfer.transaction_id)
        AND EXISTS (SELECT 1 FROM account a WHERE a.id = own_transfer.account_id));
DROP POLICY own_rows ON not_transfer;
CREATE POLICY own_rows ON not_transfer USING (user_id = current_user_id())
    WITH CHECK (user_id = current_user_id() AND EXISTS (SELECT 1 FROM transaction t WHERE t.id = not_transfer.transaction_id));

CREATE INDEX category_assignment_user ON category_assignment (user_id, assigned_at);
CREATE INDEX category_assignment_rule ON category_assignment (rule_id) WHERE rule_id IS NOT NULL;
CREATE INDEX transfer_pair_user ON transfer_pair (user_id);
CREATE INDEX own_transfer_user ON own_transfer (user_id);
CREATE INDEX not_transfer_user ON not_transfer (user_id);

-- Transfer candidates look for the opposite amount in the user's own rows, around a day.
DROP INDEX transaction_currency_amount;
CREATE INDEX transaction_user_amount ON transaction (user_id, currency, amount_minor, booked_on);
-- Every query on transactions is scoped to a user: transaction_user_booked serves the day ranges.
DROP INDEX transaction_booked;
-- A prefix of account_user_number and account_user_external_ref.
DROP INDEX account_user;

-- audit_log is not under row-level security; the list filters by user and pages by id.
CREATE INDEX audit_log_user ON audit_log (user_id, id);
