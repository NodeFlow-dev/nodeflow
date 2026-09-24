UPDATE panel_settings
SET theme = 'dark'
WHERE theme NOT IN ('dark', 'system');

ALTER TABLE panel_settings
    DROP CONSTRAINT panel_settings_theme_check;

ALTER TABLE panel_settings
    ALTER COLUMN theme SET DEFAULT 'dark',
    ADD CONSTRAINT panel_settings_theme_check
        CHECK (theme IN ('dark', 'system'));
