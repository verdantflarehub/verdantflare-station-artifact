-- S3 standard proxy convergence: allow direct version/object deletion while keeping in-place updates forbidden,
-- and retire unreviewed soft-delete lifecycle and retention lock tables.
DROP TRIGGER artifact_versions_immutable ON station.artifact_versions;
CREATE TRIGGER artifact_versions_immutable BEFORE UPDATE OR TRUNCATE ON station.artifact_versions
    FOR EACH STATEMENT EXECUTE FUNCTION station.artifact_reject_mutation();

DROP TRIGGER artifact_objects_immutable ON station.artifact_objects;
CREATE TRIGGER artifact_objects_immutable BEFORE UPDATE OR TRUNCATE ON station.artifact_objects
    FOR EACH STATEMENT EXECUTE FUNCTION station.artifact_reject_mutation();

DROP TABLE station.artifact_retention_refs;
DROP TABLE station.artifact_retention_owners;

WITH soft_deleted AS (
    DELETE FROM station.artifact_version_lifecycle
    WHERE deleted_at IS NOT NULL
    RETURNING version_id
), purged_legacy AS (
    DELETE FROM station.artifact_legacy_refs l
    USING soft_deleted d
    WHERE l.version_id = d.version_id
), purged_versions AS (
    DELETE FROM station.artifact_versions v
    USING soft_deleted d
    WHERE v.version_id = d.version_id
    RETURNING v.organization_id, v.object_id
)
DELETE FROM station.artifact_objects o
    USING purged_versions p
    WHERE o.organization_id = p.organization_id AND o.object_id = p.object_id;

DROP TABLE station.artifact_version_lifecycle;
