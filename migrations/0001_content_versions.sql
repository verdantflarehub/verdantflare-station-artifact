CREATE TABLE station.artifact_stores (
    store_id uuid PRIMARY KEY,
    singleton boolean NOT NULL DEFAULT true UNIQUE CHECK (singleton),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE station.artifact_artifacts (
    artifact_id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    created_by uuid NOT NULL,
    next_version_no bigint NOT NULL DEFAULT 1 CHECK (next_version_no > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, artifact_id)
);

CREATE TABLE station.artifact_uploads (
    upload_id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    created_by uuid NOT NULL,
    write_id uuid NOT NULL,
    request_sha256 text NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    artifact_id uuid NOT NULL,
    version_id uuid NOT NULL UNIQUE,
    version_no bigint NOT NULL CHECK (version_no > 0),
    object_id uuid NOT NULL UNIQUE,
    source jsonb NOT NULL CHECK (jsonb_typeof(source) = 'object'),
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size bigint NOT NULL CHECK (size >= 0 AND size <= 9007199254740991),
    mime text NOT NULL,
    state text NOT NULL DEFAULT 'prepared' CHECK (state IN ('prepared', 'committed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    committed_at timestamptz,
    UNIQUE (organization_id, created_by, write_id),
    UNIQUE (artifact_id, version_no),
    UNIQUE (organization_id, upload_id),
    FOREIGN KEY (organization_id, artifact_id) REFERENCES station.artifact_artifacts(organization_id, artifact_id),
    CHECK ((state = 'committed') = (committed_at IS NOT NULL))
);

CREATE TABLE station.artifact_objects (
    object_id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    storage_key text NOT NULL UNIQUE,
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size bigint NOT NULL CHECK (size >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, object_id)
);

CREATE TABLE station.artifact_versions (
    version_id uuid PRIMARY KEY,
    artifact_id uuid NOT NULL,
    organization_id uuid NOT NULL,
    version_no bigint NOT NULL CHECK (version_no > 0),
    object_id uuid NOT NULL UNIQUE,
    source jsonb NOT NULL CHECK (jsonb_typeof(source) = 'object'),
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size bigint NOT NULL CHECK (size >= 0),
    mime text NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (artifact_id, version_no),
    UNIQUE (organization_id, version_id),
    FOREIGN KEY (organization_id, artifact_id) REFERENCES station.artifact_artifacts(organization_id, artifact_id),
    FOREIGN KEY (organization_id, object_id) REFERENCES station.artifact_objects(organization_id, object_id)
);

-- Mutable lifecycle is separate from immutable version metadata.
CREATE TABLE station.artifact_version_lifecycle (
    version_id uuid PRIMARY KEY REFERENCES station.artifact_versions(version_id),
    deleted_at timestamptz,
    deleted_by uuid,
    delete_reason text,
    CHECK ((deleted_at IS NULL) = (deleted_by IS NULL))
);

CREATE TABLE station.artifact_retention_owners (
    organization_id uuid NOT NULL,
    owner_kind text NOT NULL CHECK (owner_kind IN ('project_revision', 'asset_version', 'pending_commit')),
    owner_id uuid NOT NULL,
    commit_id uuid NOT NULL,
    request_sha256 text NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    released_at timestamptz,
    PRIMARY KEY (organization_id, owner_kind, owner_id)
);

CREATE TABLE station.artifact_retention_refs (
    organization_id uuid NOT NULL,
    owner_kind text NOT NULL,
    owner_id uuid NOT NULL,
    version_id uuid NOT NULL,
    PRIMARY KEY (organization_id, owner_kind, owner_id, version_id),
    FOREIGN KEY (organization_id, owner_kind, owner_id) REFERENCES station.artifact_retention_owners(organization_id, owner_kind, owner_id),
    FOREIGN KEY (organization_id, version_id) REFERENCES station.artifact_versions(organization_id, version_id)
);
CREATE INDEX artifact_retention_version_idx ON station.artifact_retention_refs(version_id);

CREATE TABLE station.artifact_legacy_refs (
    organization_id uuid NOT NULL,
    service_id text NOT NULL,
    legacy_artifact_id text NOT NULL,
    legacy_version_id text NOT NULL,
    version_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, service_id, legacy_artifact_id, legacy_version_id),
    FOREIGN KEY (organization_id, version_id) REFERENCES station.artifact_versions(organization_id, version_id)
);

CREATE TABLE station.artifact_events (
    event_id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    subject_id uuid NOT NULL,
    action text NOT NULL,
    resource_id uuid NOT NULL,
    request_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION station.artifact_reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'immutable Artifact record' USING ERRCODE = '55000';
END;
$$;
CREATE TRIGGER artifact_versions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON station.artifact_versions
    FOR EACH STATEMENT EXECUTE FUNCTION station.artifact_reject_mutation();
CREATE TRIGGER artifact_objects_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON station.artifact_objects
    FOR EACH STATEMENT EXECUTE FUNCTION station.artifact_reject_mutation();
CREATE TRIGGER artifact_events_append_only BEFORE UPDATE OR DELETE OR TRUNCATE ON station.artifact_events
    FOR EACH STATEMENT EXECUTE FUNCTION station.artifact_reject_mutation();
