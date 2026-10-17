-- System categories (002) stay read-only; the user's own categories and their look of the
-- system ones live in the user's context (ADR 205).
CREATE TABLE user_category (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    code                    text NOT NULL,
    parent_category_id      uuid REFERENCES category (id),
    parent_user_category_id uuid REFERENCES user_category (id),
    name                    text NOT NULL,
    kind                    text NOT NULL CHECK (kind IN ('income', 'expense', 'financial', 'transfer')),
    color_key               text,
    icon                    text,
    budget_group_id         uuid REFERENCES budget_group (id),
    sort                    int NOT NULL DEFAULT 0,
    created_at              timestamptz NOT NULL DEFAULT now(),
    parent_id               uuid GENERATED ALWAYS AS (coalesce(parent_category_id, parent_user_category_id)) STORED,
    UNIQUE (user_id, code),
    CHECK (num_nonnulls(parent_category_id, parent_user_category_id) <= 1)
);

CREATE TABLE category_preference (
    user_id         uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    category_id     uuid NOT NULL REFERENCES category (id),
    color_key       text,
    icon            text,
    budget_group_id uuid REFERENCES budget_group (id),
    PRIMARY KEY (user_id, category_id)
);

-- Each reference points at a system or a user category; category_id stays the one id readers use.
ALTER TABLE category_assignment DROP CONSTRAINT category_assignment_pkey;
ALTER TABLE category_assignment RENAME COLUMN category_id TO system_category_id;
ALTER TABLE category_assignment ALTER COLUMN system_category_id DROP NOT NULL;
ALTER TABLE category_assignment ADD COLUMN user_category_id uuid REFERENCES user_category (id);
ALTER TABLE category_assignment ADD COLUMN category_id uuid
    GENERATED ALWAYS AS (coalesce(system_category_id, user_category_id)) STORED;
ALTER TABLE category_assignment ADD CHECK (num_nonnulls(system_category_id, user_category_id) = 1);
DROP INDEX category_assignment_category;
CREATE UNIQUE INDEX category_assignment_line ON category_assignment (transaction_id, category_id);
CREATE INDEX category_assignment_category ON category_assignment (category_id);

ALTER TABLE rule RENAME COLUMN category_id TO system_category_id;
ALTER TABLE rule ALTER COLUMN system_category_id DROP NOT NULL;
ALTER TABLE rule ADD COLUMN user_category_id uuid REFERENCES user_category (id);
ALTER TABLE rule ADD COLUMN category_id uuid GENERATED ALWAYS AS (coalesce(system_category_id, user_category_id)) STORED;
ALTER TABLE rule ADD CHECK (num_nonnulls(system_category_id, user_category_id) = 1);

DROP INDEX budget_month_category;
ALTER TABLE budget DROP CONSTRAINT budget_check;
ALTER TABLE budget RENAME COLUMN category_id TO system_category_id;
ALTER TABLE budget ADD COLUMN user_category_id uuid REFERENCES user_category (id);
ALTER TABLE budget ADD COLUMN category_id uuid GENERATED ALWAYS AS (coalesce(system_category_id, user_category_id)) STORED;
ALTER TABLE budget ADD CHECK (num_nonnulls(system_category_id, user_category_id, budget_group_id) = 1);
CREATE UNIQUE INDEX budget_month_category ON budget (month, category_id) WHERE category_id IS NOT NULL;

-- The one taxonomy every reader sees. coin-book has a single user, so preferences join without a user filter.
CREATE VIEW any_category AS
SELECT c.id, c.code, c.parent_id, c.name_cs, c.name_sk, c.kind,
       coalesce(p.color_key, c.color_key) AS color_key, coalesce(p.icon, c.icon) AS icon,
       coalesce(p.budget_group_id, c.budget_group_id) AS budget_group_id, c.sort, false AS custom
FROM category c LEFT JOIN category_preference p ON p.category_id = c.id
UNION ALL
SELECT u.id, u.code, u.parent_id, u.name, u.name, u.kind, u.color_key, u.icon, u.budget_group_id, 1000 + u.sort, true
FROM user_category u;
