-- Kept apart from 014 because development databases applied an earlier draft of it;
-- every statement here also holds on a database where 014 already had them.
ALTER TABLE account DROP CONSTRAINT account_kind_check;
ALTER TABLE account ADD CONSTRAINT account_kind_check
    CHECK (kind IN ('current', 'savings', 'pocket', 'fund', 'broker', 'cash', 'card',
                    'p2p', 'pension', 'property', 'other_asset', 'mortgage', 'loan', 'loan_reserve'));
ALTER TABLE account DROP CONSTRAINT account_institution_check;
ALTER TABLE account ADD CONSTRAINT account_institution_check
    CHECK (institution IN ('mbank_cz', 'mbank_sk', 'revolut', 'ibkr', 'fingood', 'investown', 'portu', 'other'));

-- A bank may name the payment's purpose instead of its counterparty account (Air Bank: "Úhrada do
-- rezervy"), so one-leg transfers are also recognised by operation type.
ALTER TABLE own_transfer DROP CONSTRAINT own_transfer_matched_by_check;
ALTER TABLE own_transfer ADD CONSTRAINT own_transfer_matched_by_check
    CHECK (matched_by IN ('number', 'counterparty', 'manual', 'operation'));

-- loan: a split the schedule wrote; the classifier does not learn from it.
ALTER TABLE category_assignment DROP CONSTRAINT category_assignment_source_check;
ALTER TABLE category_assignment ADD CONSTRAINT category_assignment_source_check
    CHECK (source IN ('rule', 'manual', 'bank', 'model', 'loan'));

-- One reserve per mortgage: two would make transfers into it ambiguous.
CREATE UNIQUE INDEX IF NOT EXISTS account_one_loan_reserve ON account (parent_id) WHERE kind = 'loan_reserve';

INSERT INTO category (code, parent_id, name_cs, name_sk, name_en, kind, sort)
SELECT 'housing.mortgage_interest', p.id, 'Úroky z hypotéky', 'Úroky z hypotéky', 'Mortgage interest', p.kind, 80
FROM category p WHERE p.code = 'housing'
    AND NOT EXISTS (SELECT 1 FROM category c WHERE c.code = 'housing.mortgage_interest');
