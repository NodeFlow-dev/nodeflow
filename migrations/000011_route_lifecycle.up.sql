ALTER TABLE routes
    ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    ADD COLUMN deployed boolean NOT NULL DEFAULT false,
    ADD COLUMN deployment_state varchar(16) NOT NULL DEFAULT 'draft'
        CHECK (deployment_state IN ('draft','pending','active','disabled','failed','deleting')),
    ADD COLUMN deployment_error varchar(2000) NOT NULL DEFAULT '',
    ADD COLUMN desired_revision bigint,
    ADD COLUMN applied_revision bigint,
    ADD COLUMN desired_fingerprint varchar(64) NOT NULL DEFAULT ''
        CHECK (desired_fingerprint = '' OR desired_fingerprint ~ '^[0-9a-f]{64}$'),
    ADD COLUMN deployed_fingerprint varchar(64) NOT NULL DEFAULT ''
        CHECK (deployed_fingerprint = '' OR deployed_fingerprint ~ '^[0-9a-f]{64}$'),
    ADD COLUMN delete_pending boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT routes_desired_revision_fk
        FOREIGN KEY (node_id,desired_revision)
        REFERENCES config_revisions(node_id,revision),
    ADD CONSTRAINT routes_applied_revision_fk
        FOREIGN KEY (node_id,applied_revision)
        REFERENCES config_revisions(node_id,revision),
    ADD CONSTRAINT routes_delete_pending_check
        CHECK (NOT delete_pending OR NOT enabled);

-- Preserve the live state of nodes that predate route lifecycle tracking.
-- Both metadata shapes are accepted because older renderers exposed
-- runtime_names while newer ones also expose route_backends.
WITH state AS (
    SELECT s.node_id,s.desired_revision,s.actual_revision,s.state,s.last_error,
           desired.metadata AS desired_metadata,
           actual.metadata AS actual_metadata
    FROM node_config_state AS s
    LEFT JOIN config_revisions AS desired
      ON desired.node_id=s.node_id AND desired.revision=s.desired_revision
    LEFT JOIN config_revisions AS actual
      ON actual.node_id=s.node_id AND actual.revision=s.actual_revision
), membership AS (
    SELECT r.id,r.node_id,r.enabled,state.desired_revision,state.actual_revision,
           state.state AS node_state,state.last_error,
           encode(digest(jsonb_build_object(
             'listener_ip',r.listener_ip,'listener_port',r.listener_port,
             'snis',COALESCE((SELECT jsonb_agg(sni ORDER BY position) FROM route_snis WHERE route_id=r.id),'[]'::jsonb),
             'fallback',r.fallback,'target_type',r.target_type,'target_host',r.target_host,
             'target_port',r.target_port,'unix_socket_path',r.unix_socket_path,
             'proxy_protocol',r.proxy_protocol,'quota_bytes',r.quota_bytes,
             'quota_action',r.quota_action,'enabled',r.enabled,'custom_fragment',r.custom_fragment
           )::text,'sha256'),'hex') AS intent_fingerprint,
           (
             COALESCE(state.actual_metadata->'route_backends','{}'::jsonb) ? r.id::text OR
             EXISTS (
               SELECT 1
               FROM jsonb_array_elements(
                 CASE WHEN jsonb_typeof(state.actual_metadata->'runtime_names')='array'
                      THEN state.actual_metadata->'runtime_names' ELSE '[]'::jsonb END
               ) AS runtime
               WHERE runtime->>'route_id'=r.id::text
             )
           ) AS is_deployed
    FROM routes AS r
    LEFT JOIN state ON state.node_id=r.node_id
)
UPDATE routes AS route
SET deployed=membership.is_deployed,
    desired_revision=CASE WHEN route.enabled THEN membership.desired_revision END,
    applied_revision=membership.actual_revision,
    desired_fingerprint=membership.intent_fingerprint,
    deployed_fingerprint=CASE WHEN membership.is_deployed THEN membership.intent_fingerprint ELSE '' END,
    deployment_state=CASE
      WHEN route.enabled AND membership.node_state IN ('failed','rolled_back') THEN 'failed'
      WHEN route.enabled AND membership.is_deployed
           AND membership.desired_revision IS NOT DISTINCT FROM membership.actual_revision THEN 'active'
      WHEN route.enabled THEN 'pending'
      WHEN membership.is_deployed THEN 'failed'
      ELSE 'draft'
    END,
    deployment_error=CASE
      WHEN route.enabled AND membership.node_state IN ('failed','rolled_back')
        THEN COALESCE(NULLIF(membership.last_error,''),'apply_failed')
      WHEN NOT route.enabled AND membership.is_deployed THEN 'observed_route_state_mismatch'
      ELSE ''
    END
FROM membership
WHERE route.id=membership.id;

CREATE INDEX routes_node_deployment_idx
    ON routes(node_id,deployment_state,enabled);
CREATE INDEX routes_pending_delete_idx
    ON routes(node_id,desired_revision) WHERE delete_pending;
