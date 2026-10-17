-- 'model': assigned by the learned classifier above the auto threshold; the user confirms it later.
ALTER TABLE category_assignment DROP CONSTRAINT category_assignment_source_check;
ALTER TABLE category_assignment ADD CONSTRAINT category_assignment_source_check
    CHECK (source IN ('rule', 'manual', 'bank', 'model'));

-- Single-user preferences; value is JSON so a setting keeps its type.
CREATE TABLE app_setting (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
