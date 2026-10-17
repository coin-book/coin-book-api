DROP VIEW any_category;

DELETE FROM budget WHERE user_category_id IS NOT NULL;
DROP INDEX budget_month_category;
ALTER TABLE budget DROP COLUMN category_id;
ALTER TABLE budget DROP COLUMN user_category_id;
ALTER TABLE budget RENAME COLUMN system_category_id TO category_id;
ALTER TABLE budget ADD CONSTRAINT budget_check CHECK (num_nonnulls(category_id, budget_group_id) = 1);
CREATE UNIQUE INDEX budget_month_category ON budget (month, category_id) WHERE category_id IS NOT NULL;

DELETE FROM rule WHERE user_category_id IS NOT NULL;
ALTER TABLE rule DROP COLUMN category_id;
ALTER TABLE rule DROP COLUMN user_category_id;
ALTER TABLE rule RENAME COLUMN system_category_id TO category_id;
ALTER TABLE rule ALTER COLUMN category_id SET NOT NULL;

DELETE FROM category_assignment WHERE user_category_id IS NOT NULL;
DROP INDEX category_assignment_category;
DROP INDEX category_assignment_line;
ALTER TABLE category_assignment DROP COLUMN category_id;
ALTER TABLE category_assignment DROP COLUMN user_category_id;
ALTER TABLE category_assignment RENAME COLUMN system_category_id TO category_id;
ALTER TABLE category_assignment ALTER COLUMN category_id SET NOT NULL;
ALTER TABLE category_assignment ADD PRIMARY KEY (transaction_id, category_id);
CREATE INDEX category_assignment_category ON category_assignment (category_id);

DROP TABLE category_preference;
DROP TABLE user_category;
