-- Rolling back would pool every user's rows together; refuse unless at most one user exists.
DO $$
BEGIN
    IF (SELECT count(*) FROM app_user) > 1 THEN
        RAISE EXCEPTION 'coin-book 008 down: several users exist; their data cannot be merged back into one';
    END IF;
END $$;

ALTER VIEW any_category RESET (security_invoker);
ALTER VIEW transfer_leg RESET (security_invoker);

DROP POLICY own_rows ON not_transfer;
ALTER TABLE not_transfer DISABLE ROW LEVEL SECURITY;
ALTER TABLE not_transfer NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON own_transfer;
ALTER TABLE own_transfer DISABLE ROW LEVEL SECURITY;
ALTER TABLE own_transfer NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON transfer_pair;
ALTER TABLE transfer_pair DISABLE ROW LEVEL SECURITY;
ALTER TABLE transfer_pair NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON category_assignment;
ALTER TABLE category_assignment DISABLE ROW LEVEL SECURITY;
ALTER TABLE category_assignment NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON category_preference;
ALTER TABLE category_preference DISABLE ROW LEVEL SECURITY;
ALTER TABLE category_preference NO FORCE ROW LEVEL SECURITY;
ALTER TABLE category_preference ALTER COLUMN user_id DROP DEFAULT;
DROP POLICY own_rows ON user_category;
ALTER TABLE user_category DISABLE ROW LEVEL SECURITY;
ALTER TABLE user_category NO FORCE ROW LEVEL SECURITY;
ALTER TABLE user_category ALTER COLUMN user_id DROP DEFAULT;
DROP POLICY own_rows ON own_transfer_rule;
ALTER TABLE own_transfer_rule DISABLE ROW LEVEL SECURITY;
ALTER TABLE own_transfer_rule NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON account_alias;
ALTER TABLE account_alias DISABLE ROW LEVEL SECURITY;
ALTER TABLE account_alias NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON app_setting;
ALTER TABLE app_setting DISABLE ROW LEVEL SECURITY;
ALTER TABLE app_setting NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON budget;
ALTER TABLE budget DISABLE ROW LEVEL SECURITY;
ALTER TABLE budget NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON rule;
ALTER TABLE rule DISABLE ROW LEVEL SECURITY;
ALTER TABLE rule NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON transaction;
ALTER TABLE transaction DISABLE ROW LEVEL SECURITY;
ALTER TABLE transaction NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON import_batch;
ALTER TABLE import_batch DISABLE ROW LEVEL SECURITY;
ALTER TABLE import_batch NO FORCE ROW LEVEL SECURITY;
DROP POLICY own_rows ON account;
ALTER TABLE account DISABLE ROW LEVEL SECURITY;
ALTER TABLE account NO FORCE ROW LEVEL SECURITY;

DROP INDEX budget_month_category;
DROP INDEX budget_month_group;
CREATE UNIQUE INDEX budget_month_category ON budget (month, category_id) WHERE category_id IS NOT NULL;
CREATE UNIQUE INDEX budget_month_group ON budget (month, budget_group_id) WHERE budget_group_id IS NOT NULL;
ALTER TABLE own_transfer_rule DROP CONSTRAINT own_transfer_rule_pkey;
ALTER TABLE own_transfer_rule ADD PRIMARY KEY (counterparty);
ALTER TABLE app_setting DROP CONSTRAINT app_setting_pkey;
ALTER TABLE app_setting ADD PRIMARY KEY (key);
DROP INDEX import_batch_committed_file;
CREATE UNIQUE INDEX import_batch_committed_file ON import_batch (file_hash) WHERE status = 'committed';
ALTER TABLE account_alias DROP CONSTRAINT account_alias_user_identity;
ALTER TABLE account_alias ADD CONSTRAINT account_alias_institution_identity_key UNIQUE (institution, identity);
ALTER TABLE account DROP CONSTRAINT account_user_number;
ALTER TABLE account DROP CONSTRAINT account_user_external_ref;
ALTER TABLE account ADD CONSTRAINT account_account_number_key UNIQUE (account_number);
ALTER TABLE account ADD CONSTRAINT account_external_ref_key UNIQUE (external_ref);

ALTER TABLE own_transfer_rule DROP COLUMN user_id;
ALTER TABLE account_alias DROP COLUMN user_id;
ALTER TABLE app_setting DROP COLUMN user_id;
ALTER TABLE audit_log DROP COLUMN user_id;
ALTER TABLE agent_token DROP COLUMN user_id;
ALTER TABLE budget DROP COLUMN user_id;
ALTER TABLE rule DROP COLUMN user_id;
ALTER TABLE transaction DROP COLUMN user_id;
ALTER TABLE import_batch DROP COLUMN user_id;
ALTER TABLE account DROP COLUMN user_id;

ALTER TABLE app_user DROP COLUMN google_sub;
ALTER TABLE app_user ALTER COLUMN password_hash SET NOT NULL;

DROP FUNCTION current_user_id();

ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM coinbook_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM coinbook_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM coinbook_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM coinbook_app;
REVOKE USAGE ON SCHEMA public FROM coinbook_app;
DO $$ BEGIN EXECUTE format('REVOKE TEMP ON DATABASE %I FROM coinbook_app', current_database()); END $$;
-- The role itself is cluster-wide and may serve another database; it stays.
