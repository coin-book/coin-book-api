DELETE FROM category WHERE parent_id IS NOT NULL;
DELETE FROM category;
DELETE FROM budget_group;
