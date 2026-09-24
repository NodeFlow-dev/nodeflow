CREATE TABLE node_firewall_policies (
    node_id uuid PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    mode varchar(16) NOT NULL DEFAULT 'observe'
        CHECK (mode IN ('off','observe','apply')),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO node_firewall_policies(node_id)
SELECT id FROM nodes
ON CONFLICT DO NOTHING;
