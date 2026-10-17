-- Several users, each seeing only their own data (ADR 208). Isolation is enforced by row-level
-- security keyed on app.user_id, which the API sets on the connection for every signed-in request.
-- Transaction ids now include the user, so this migration refuses pre-release data instead of
-- leaving rows whose ids would never match a re-import.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM transaction) THEN
        RAISE EXCEPTION 'coin-book 008: pre-release transactions present; reset the database and import again (ADR 208)';
    END IF;
END $$;

CREATE FUNCTION current_user_id() RETURNS uuid
    LANGUAGE sql STABLE
    RETURN nullif(current_setting('app.user_id', true), '')::uuid;

-- The API logs in as this role, never as the (super)user that owns the schema, because
-- superusers bypass row-level security. The API grants it LOGIN with its own password at
-- start (db.PrepareAppRole). TEMP is for the import staging table, MAINTAIN on transaction
-- for the ANALYZE an import runs.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'coinbook_app') THEN
        CREATE ROLE coinbook_app NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB;
    END IF;
    EXECUTE format('GRANT TEMP ON DATABASE %I TO coinbook_app', current_database());
END $$;
GRANT USAGE ON SCHEMA public TO coinbook_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO coinbook_app;
REVOKE ALL ON schema_migrations FROM coinbook_app;
GRANT MAINTAIN ON transaction TO coinbook_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO coinbook_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO coinbook_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO coinbook_app;

ALTER TABLE app_user ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE app_user ADD COLUMN google_sub text UNIQUE;

ALTER TABLE account ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();
ALTER TABLE import_batch ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();
ALTER TABLE transaction ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();
ALTER TABLE rule ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();
ALTER TABLE budget ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();
ALTER TABLE agent_token ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();
ALTER TABLE audit_log ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();
ALTER TABLE app_setting ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();
ALTER TABLE account_alias ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();
ALTER TABLE own_transfer_rule ADD COLUMN user_id uuid REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id();

-- Rows written before this migration belong to the one user that could exist.
UPDATE account SET user_id = (SELECT id FROM app_user ORDER BY created_at LIMIT 1) WHERE user_id IS NULL;
UPDATE import_batch SET user_id = (SELECT id FROM app_user ORDER BY created_at LIMIT 1) WHERE user_id IS NULL;
UPDATE rule SET user_id = (SELECT id FROM app_user ORDER BY created_at LIMIT 1) WHERE user_id IS NULL;
UPDATE budget SET user_id = (SELECT id FROM app_user ORDER BY created_at LIMIT 1) WHERE user_id IS NULL;
UPDATE agent_token SET user_id = (SELECT id FROM app_user ORDER BY created_at LIMIT 1) WHERE user_id IS NULL;
UPDATE audit_log SET user_id = (SELECT id FROM app_user ORDER BY created_at LIMIT 1) WHERE user_id IS NULL;
UPDATE app_setting SET user_id = (SELECT id FROM app_user ORDER BY created_at LIMIT 1) WHERE user_id IS NULL;
UPDATE account_alias SET user_id = (SELECT id FROM app_user ORDER BY created_at LIMIT 1) WHERE user_id IS NULL;
UPDATE own_transfer_rule SET user_id = (SELECT id FROM app_user ORDER BY created_at LIMIT 1) WHERE user_id IS NULL;

ALTER TABLE account ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE import_batch ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE transaction ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE rule ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE budget ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE agent_token ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE audit_log ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE app_setting ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE account_alias ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE own_transfer_rule ALTER COLUMN user_id SET NOT NULL;

CREATE INDEX account_user ON account (user_id);
CREATE INDEX import_batch_user ON import_batch (user_id);
CREATE INDEX transaction_user_booked ON transaction (user_id, booked_on);
CREATE INDEX rule_user ON rule (user_id);
CREATE INDEX budget_user ON budget (user_id);
CREATE INDEX agent_token_user ON agent_token (user_id);

-- Uniqueness that was global is now per user.
ALTER TABLE account DROP CONSTRAINT account_account_number_key;
ALTER TABLE account DROP CONSTRAINT account_external_ref_key;
ALTER TABLE account ADD CONSTRAINT account_user_number UNIQUE (user_id, account_number);
ALTER TABLE account ADD CONSTRAINT account_user_external_ref UNIQUE (user_id, external_ref);
ALTER TABLE account_alias DROP CONSTRAINT account_alias_institution_identity_key;
ALTER TABLE account_alias ADD CONSTRAINT account_alias_user_identity UNIQUE (user_id, institution, identity);
DROP INDEX import_batch_committed_file;
CREATE UNIQUE INDEX import_batch_committed_file ON import_batch (user_id, file_hash) WHERE status = 'committed';
ALTER TABLE app_setting DROP CONSTRAINT app_setting_pkey;
ALTER TABLE app_setting ADD PRIMARY KEY (user_id, key);
ALTER TABLE own_transfer_rule DROP CONSTRAINT own_transfer_rule_pkey;
ALTER TABLE own_transfer_rule ADD PRIMARY KEY (user_id, counterparty);
DROP INDEX budget_month_category;
DROP INDEX budget_month_group;
CREATE UNIQUE INDEX budget_month_category ON budget (user_id, month, category_id) WHERE category_id IS NOT NULL;
CREATE UNIQUE INDEX budget_month_group ON budget (user_id, month, budget_group_id) WHERE budget_group_id IS NOT NULL;

-- agent_token and audit_log stay without policies: a token is looked up by its hash before
-- the user is known; internal/domain/agentaccess filters them by user_id itself.
ALTER TABLE account ENABLE ROW LEVEL SECURITY;
ALTER TABLE account FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON account USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE import_batch ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_batch FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON import_batch USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE transaction ENABLE ROW LEVEL SECURITY;
ALTER TABLE transaction FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON transaction USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE rule FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON rule USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE budget ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON budget USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE app_setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE app_setting FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON app_setting USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE account_alias ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_alias FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON account_alias USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE own_transfer_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE own_transfer_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON own_transfer_rule USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE user_category ALTER COLUMN user_id SET DEFAULT current_user_id();
ALTER TABLE user_category ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_category FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON user_category USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
ALTER TABLE category_preference ALTER COLUMN user_id SET DEFAULT current_user_id();
ALTER TABLE category_preference ENABLE ROW LEVEL SECURITY;
ALTER TABLE category_preference FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON category_preference USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());

-- Rows hanging off a transaction belong to whoever can see that transaction.
ALTER TABLE category_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE category_assignment FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON category_assignment
    USING (EXISTS (SELECT 1 FROM transaction t WHERE t.id = category_assignment.transaction_id))
    WITH CHECK (EXISTS (SELECT 1 FROM transaction t WHERE t.id = category_assignment.transaction_id));
ALTER TABLE transfer_pair ENABLE ROW LEVEL SECURITY;
ALTER TABLE transfer_pair FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON transfer_pair
    USING (EXISTS (SELECT 1 FROM transaction t WHERE t.id = transfer_pair.out_tx_id))
    WITH CHECK (EXISTS (SELECT 1 FROM transaction t WHERE t.id = transfer_pair.out_tx_id)
        AND EXISTS (SELECT 1 FROM transaction t WHERE t.id = transfer_pair.in_tx_id));
ALTER TABLE own_transfer ENABLE ROW LEVEL SECURITY;
ALTER TABLE own_transfer FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON own_transfer
    USING (EXISTS (SELECT 1 FROM transaction t WHERE t.id = own_transfer.transaction_id))
    WITH CHECK (EXISTS (SELECT 1 FROM transaction t WHERE t.id = own_transfer.transaction_id));
ALTER TABLE not_transfer ENABLE ROW LEVEL SECURITY;
ALTER TABLE not_transfer FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON not_transfer
    USING (EXISTS (SELECT 1 FROM transaction t WHERE t.id = not_transfer.transaction_id))
    WITH CHECK (EXISTS (SELECT 1 FROM transaction t WHERE t.id = not_transfer.transaction_id));

-- A view runs with its owner's rights by default, which would skip the policies.
ALTER VIEW any_category SET (security_invoker = true);
ALTER VIEW transfer_leg SET (security_invoker = true);
