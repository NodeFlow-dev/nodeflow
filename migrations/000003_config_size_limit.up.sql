ALTER TABLE config_revisions
    ADD CONSTRAINT config_revisions_config_size
    CHECK (octet_length(config) <= 524288);
