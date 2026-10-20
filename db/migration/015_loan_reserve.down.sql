DROP INDEX account_one_loan_reserve;
DELETE FROM category_assignment WHERE source = 'loan';
ALTER TABLE category_assignment DROP CONSTRAINT category_assignment_source_check;
ALTER TABLE category_assignment ADD CONSTRAINT category_assignment_source_check
    CHECK (source IN ('rule', 'manual', 'bank', 'model'));
DELETE FROM own_transfer WHERE matched_by = 'operation';
ALTER TABLE own_transfer DROP CONSTRAINT own_transfer_matched_by_check;
ALTER TABLE own_transfer ADD CONSTRAINT own_transfer_matched_by_check
    CHECK (matched_by IN ('number', 'counterparty', 'manual'));
DELETE FROM account WHERE kind = 'loan_reserve' OR institution = 'portu';
ALTER TABLE account DROP CONSTRAINT account_institution_check;
ALTER TABLE account ADD CONSTRAINT account_institution_check
    CHECK (institution IN ('mbank_cz', 'mbank_sk', 'revolut', 'ibkr', 'fingood', 'investown', 'other'));
ALTER TABLE account DROP CONSTRAINT account_kind_check;
ALTER TABLE account ADD CONSTRAINT account_kind_check
    CHECK (kind IN ('current', 'savings', 'pocket', 'fund', 'broker', 'cash', 'card',
                    'p2p', 'pension', 'property', 'other_asset', 'mortgage', 'loan'));
