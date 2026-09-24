-- Route fingerprints are projections of immutable revision metadata. Renderer
-- v6 added operator-visible route fields, but pre-v6 desired fingerprints were
-- left in routes when an unrelated route published the next node revision.
WITH desired AS (
    SELECT r.id,
           revision.metadata->'route_fingerprints'->>r.id::text AS fingerprint
    FROM routes AS r
    JOIN config_revisions AS revision
      ON revision.node_id=r.node_id AND revision.revision=r.desired_revision
)
UPDATE routes AS route
SET desired_fingerprint=desired.fingerprint
FROM desired
WHERE route.id=desired.id
  AND desired.fingerprint ~ '^[0-9a-f]{64}$'
  AND route.desired_fingerprint IS DISTINCT FROM desired.fingerprint;

WITH actual AS (
    SELECT r.id,
           revision.metadata->'route_fingerprints'->>r.id::text AS fingerprint
    FROM routes AS r
    JOIN config_revisions AS revision
      ON revision.node_id=r.node_id AND revision.revision=r.applied_revision
)
UPDATE routes AS route
SET deployed_fingerprint=actual.fingerprint
FROM actual
WHERE route.id=actual.id
  AND actual.fingerprint ~ '^[0-9a-f]{64}$'
  AND route.deployed_fingerprint IS DISTINCT FROM actual.fingerprint;
