package models

// ConfigMeta is intentionally removed: the v2.1 EpochGenerator it persisted
// for was retired (ConfigEventBus.CurrentEpoch is a hardcoded 0 API-compat
// stub). The historical config_meta table is left in place for existing
// databases and is simply no longer registered for AutoMigrate.
