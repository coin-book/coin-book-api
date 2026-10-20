DELETE FROM category_assignment WHERE system_category_id IN (SELECT id FROM category WHERE code = 'housing.mortgage_interest');
DELETE FROM rule WHERE system_category_id IN (SELECT id FROM category WHERE code = 'housing.mortgage_interest');
DELETE FROM budget WHERE system_category_id IN (SELECT id FROM category WHERE code = 'housing.mortgage_interest');
DELETE FROM category_preference WHERE category_id IN (SELECT id FROM category WHERE code = 'housing.mortgage_interest');
UPDATE user_category SET parent_category_id = NULL
    WHERE parent_category_id IN (SELECT id FROM category WHERE code = 'housing.mortgage_interest');
DELETE FROM category WHERE code = 'housing.mortgage_interest';
DROP TABLE loan_payment;
DROP TABLE loan_terms;
DROP TABLE p2p_position;
DROP TABLE account_valuation;
DELETE FROM account WHERE kind IN ('p2p', 'pension', 'property', 'other_asset', 'mortgage', 'loan')
    OR institution IN ('fingood', 'investown');
ALTER TABLE account DROP CONSTRAINT account_institution_check;
ALTER TABLE account ADD CONSTRAINT account_institution_check
    CHECK (institution IN ('mbank_cz', 'mbank_sk', 'revolut', 'ibkr', 'other'));
ALTER TABLE account DROP CONSTRAINT account_kind_check;
ALTER TABLE account ADD CONSTRAINT account_kind_check
    CHECK (kind IN ('current', 'savings', 'pocket', 'fund', 'broker', 'cash', 'card'));
