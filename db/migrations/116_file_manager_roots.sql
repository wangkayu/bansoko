-- Forward-only migration 116: file-manager library roots.
--
-- The file manager works only inside directories the owner added: the roots
-- are the authorization boundary for every read and every write, and a path
-- outside all of them is neither listed nor accepted as a target. This
-- replaces the earlier boundary (the filesystem root, with the container
-- user's own permissions deciding everything below it).
--
-- A root is stored by its absolute container path; the owner UI adds and
-- removes them. Nothing else references a root: an admitted operation freezes
-- its absolute source and target paths, so removing a root bounds new work and
-- never rewrites an operation already admitted.
--
-- Rows are not migrated: every environment is pre-release and no root exists
-- yet, so the empty table is the starting state.

CREATE TABLE file_manager_roots (
    id TEXT NOT NULL,
    label TEXT NOT NULL,
    path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT file_manager_roots_pkey PRIMARY KEY (id),
    CONSTRAINT file_manager_roots_path_key UNIQUE (path),
    CONSTRAINT file_manager_roots_path_check CHECK (path LIKE '/%' AND path <> '/'),
    CONSTRAINT file_manager_roots_label_check CHECK (label <> '')
);
COMMENT ON TABLE file_manager_roots IS 'One library directory the owner authorized: the file manager reads and writes only inside these.';

GRANT SELECT, INSERT, DELETE ON TABLE file_manager_roots TO bansoko_runtime;
-- The runtime never rewrites a root row: a root is added or removed whole, so
-- the UPDATE the baseline's default privileges would inherit is revoked here
-- rather than registered in the runtime ACL inventory.
REVOKE UPDATE ON TABLE file_manager_roots FROM bansoko_runtime;
