ALTER TABLE routes
    ADD COLUMN client_upload_mbps bigint,
    ADD COLUMN client_download_mbps bigint,
    ADD CONSTRAINT routes_client_upload_mbps_check
        CHECK (client_upload_mbps IS NULL OR client_upload_mbps BETWEEN 1 AND 1000000),
    ADD CONSTRAINT routes_client_download_mbps_check
        CHECK (client_download_mbps IS NULL OR client_download_mbps BETWEEN 1 AND 1000000);
