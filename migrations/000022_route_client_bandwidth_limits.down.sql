ALTER TABLE routes
    DROP CONSTRAINT routes_client_download_mbps_check,
    DROP CONSTRAINT routes_client_upload_mbps_check,
    DROP COLUMN client_download_mbps,
    DROP COLUMN client_upload_mbps;
