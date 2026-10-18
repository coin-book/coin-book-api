-- A column layout the user drew for a bank's CSV; the next file whose column names give the
-- same fingerprint is read by it without asking again.
CREATE TABLE import_template (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES app_user (id) ON DELETE CASCADE DEFAULT current_user_id(),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    fingerprint text NOT NULL,
    layout      jsonb NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, fingerprint)
);

ALTER TABLE import_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_template FORCE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON import_template USING (user_id = current_user_id()) WITH CHECK (user_id = current_user_id());
