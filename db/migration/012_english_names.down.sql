DROP VIEW any_category;
CREATE VIEW any_category AS
SELECT c.id, c.code, c.parent_id, c.name_cs, c.name_sk, c.kind,
       coalesce(p.color_key, c.color_key) AS color_key, coalesce(p.icon, c.icon) AS icon,
       coalesce(p.budget_group_id, c.budget_group_id) AS budget_group_id, c.sort, false AS custom
FROM category c LEFT JOIN category_preference p ON p.category_id = c.id
UNION ALL
SELECT u.id, u.code, u.parent_id, u.name, u.name, u.kind, u.color_key, u.icon, u.budget_group_id, 1000 + u.sort, true
FROM user_category u;
ALTER VIEW any_category SET (security_invoker = true);
ALTER TABLE category DROP COLUMN name_en;
ALTER TABLE budget_group DROP COLUMN name_en;
