DROP TABLE app_setting;
DELETE FROM category_assignment WHERE source = 'model';
ALTER TABLE category_assignment DROP CONSTRAINT category_assignment_source_check;
ALTER TABLE category_assignment ADD CONSTRAINT category_assignment_source_check
    CHECK (source IN ('rule', 'manual', 'bank'));
