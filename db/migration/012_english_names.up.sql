-- English as a third language: names of the system taxonomy. User categories have one name in any language.
ALTER TABLE budget_group ADD COLUMN name_en text;
UPDATE budget_group g SET name_en = v.name_en FROM (VALUES
    ('fixed', 'Fixed expenses'),
    ('flexible', 'Flexible expenses'),
    ('goals', 'Goals and savings')
) AS v (code, name_en) WHERE g.code = v.code;
UPDATE budget_group SET name_en = name_cs WHERE name_en IS NULL;
ALTER TABLE budget_group ALTER COLUMN name_en SET NOT NULL;

ALTER TABLE category ADD COLUMN name_en text;
UPDATE category c SET name_en = v.name_en FROM (VALUES
    ('housing', 'Housing and utilities'),
    ('groceries', 'Groceries and drugstore'),
    ('dining', 'Eating and drinking out'),
    ('transport', 'Transport'),
    ('health', 'Health and care'),
    ('children', 'Children and education'),
    ('shopping', 'Shopping'),
    ('leisure', 'Leisure and entertainment'),
    ('services', 'Services, insurance and taxes'),
    ('other', 'Other expenses'),
    ('financial', 'Savings and repayments'),
    ('income', 'Income'),
    ('transfer', 'Transfers'),
    ('housing.rent', 'Rent'),
    ('housing.hoa_fund', 'Building repair fund'),
    ('housing.energy', 'Electricity and gas'),
    ('housing.water', 'Water and sewage'),
    ('housing.sipo', 'SIPO'),
    ('housing.insurance', 'Home insurance'),
    ('housing.maintenance', 'House, garden and upkeep'),
    ('groceries.food', 'Groceries'),
    ('groceries.drugstore', 'Drugstore'),
    ('dining.restaurants', 'Restaurants'),
    ('dining.cafes', 'Cafés and bars'),
    ('dining.delivery', 'Food delivery'),
    ('transport.fuel', 'Fuel'),
    ('transport.public', 'Public transport and taxi'),
    ('transport.parking', 'Parking and tolls'),
    ('transport.service', 'Car service and parts'),
    ('transport.vignette', 'Motorway vignette'),
    ('health.pharmacy', 'Pharmacy'),
    ('health.doctor', 'Doctor'),
    ('health.beauty', 'Beauty and care'),
    ('children.childcare', 'Childcare'),
    ('children.activities', 'Clubs and activities'),
    ('children.school', 'School and supplies'),
    ('children.toys', 'Toys and things for kids'),
    ('children.education', 'Education'),
    ('shopping.clothing', 'Clothing and shoes'),
    ('shopping.electronics', 'Electronics'),
    ('shopping.home', 'Home and furnishings'),
    ('shopping.gifts', 'Gifts and charity'),
    ('leisure.events', 'Culture and events'),
    ('leisure.sport', 'Sport and hobbies'),
    ('leisure.travel', 'Travel'),
    ('leisure.media', 'Music, films and books'),
    ('leisure.subscriptions', 'Subscriptions'),
    ('services.telecom', 'Phone, internet and TV'),
    ('services.insurance', 'Personal insurance'),
    ('services.social_health', 'Health and social insurance'),
    ('services.taxes', 'Taxes'),
    ('services.bank_fees', 'Bank fees and interest'),
    ('other.cash', 'Cash withdrawal'),
    ('other.pets', 'Pets'),
    ('other.vices', 'Alcohol and tobacco'),
    ('other.misc', 'Uncategorised'),
    ('financial.savings', 'Regular savings'),
    ('financial.building', 'Building savings'),
    ('financial.pension', 'Pension savings'),
    ('financial.investments', 'Investments'),
    ('financial.loan_repayment', 'Loan repayments'),
    ('income.salary', 'Salary'),
    ('income.interest', 'Interest and returns'),
    ('income.refunds', 'Refunds'),
    ('income.loan', 'Loan payout'),
    ('income.other', 'Other income'),
    ('transfer.own', 'Between own accounts'),
    ('transfer.exchange', 'Currency exchange')
) AS v (code, name_en) WHERE c.code = v.code;
UPDATE category SET name_en = name_cs WHERE name_en IS NULL;
ALTER TABLE category ALTER COLUMN name_en SET NOT NULL;

CREATE OR REPLACE VIEW any_category AS
SELECT c.id, c.code, c.parent_id, c.name_cs, c.name_sk, c.kind,
       coalesce(p.color_key, c.color_key) AS color_key, coalesce(p.icon, c.icon) AS icon,
       coalesce(p.budget_group_id, c.budget_group_id) AS budget_group_id, c.sort, false AS custom, c.name_en
FROM category c LEFT JOIN category_preference p ON p.category_id = c.id
UNION ALL
SELECT u.id, u.code, u.parent_id, u.name, u.name, u.kind, u.color_key, u.icon, u.budget_group_id, 1000 + u.sort, true, u.name
FROM user_category u;
ALTER VIEW any_category SET (security_invoker = true);
