-- Move audit context is nullable so pre-move audit rows remain compatible.
ALTER TABLE audit_events ADD COLUMN source_id TEXT;
ALTER TABLE audit_events ADD COLUMN source_scope TEXT;
ALTER TABLE audit_events ADD COLUMN target_scope TEXT;
