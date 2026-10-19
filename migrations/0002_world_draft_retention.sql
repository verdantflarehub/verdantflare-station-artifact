-- Preserve the applied migration checksum and all existing retention identities.
-- New draft retention is authorized exclusively by World, never by Project.
ALTER TABLE station.artifact_retention_owners
    DROP CONSTRAINT artifact_retention_owners_owner_kind_check;
ALTER TABLE station.artifact_retention_owners
    ADD CONSTRAINT artifact_retention_owners_owner_kind_check
    CHECK (owner_kind IN ('project_revision', 'asset_version', 'pending_commit', 'world_draft_revision'));
