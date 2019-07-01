-- SCHEMA VERSION UPGRADE: 201607010001 -> 201805070001
--
-- connect as RDBMS superuser
--
-- install extensions required by new schema version (btree_gist)
-- or this migration script (pgcrypto)
\connect eye
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
--
-- reconnect as owner of DB 'eye'
\connect eye
--
-- create required function to index on uuid columns
CREATE OR REPLACE FUNCTION uuid_to_bytea(_uuid uuid)
  RETURNS bytea AS
  $BODY$
  select decode(replace(_uuid::text, '-', ''), 'hex');
  $BODY$
  LANGUAGE sql IMMUTABLE;
--
-- update table and column names of configuration_lookup
ALTER TABLE eye.configuration_lookup RENAME TO lookup;
ALTER TABLE eye.lookup RENAME lookup_id TO lookupID;
ALTER TABLE eye.lookup RENAME host_id TO hostID;
--
-- update table and column names of configuration_items
ALTER TABLE eye.configuration_items RENAME TO configurations;
ALTER TABLE eye.configurations RENAME configuration_item_id TO configurationID;
ALTER TABLE eye.configurations RENAME lookup_id TO lookupID;
--
-- recreate foreign key constraint with new name
ALTER TABLE eye.configurations DROP CONSTRAINT configuration_items_lookup_id_fkey;
ALTER TABLE eye.configurations ADD CONSTRAINT configurations_lookupid_fkey FOREIGN KEY(lookupID) REFERENCES eye.lookup( lookupID );
--
-- recreate index with new name
DROP INDEX _item_lookup;
CREATE INDEX _configurations_lookup ON eye.configurations (
  lookupID,
  configurationID
);
--
-- create new table configurations_data with its specialty indices
CREATE TABLE IF NOT EXISTS eye.configurations_data (
  dataID                  uuid            PRIMARY KEY,
  configurationID         uuid            NOT NULL REFERENCES eye.configurations( configurationID ) ON DELETE RESTRICT,
  validity                tstzrange       NOT NULL DEFAULT tstzrange(NOW()::timestamptz(3), 'infinity', '[]'),
  configuration           jsonb           NOT NULL,
  EXCLUDE USING gist (uuid_to_bytea(configurationID) WITH =, validity WITH &&),
  CONSTRAINT validFrom_utc CHECK( EXTRACT( TIMEZONE FROM lower( validity ) ) = '0' ),
  CONSTRAINT validUntil_utc CHECK( EXTRACT( TIMEZONE FROM upper( validity ) ) = '0' )
);
CREATE UNIQUE INDEX _configuration_data ON eye.configurations_data (
  dataID,
  configurationID
);
CREATE INDEX _configurations_data_range_query ON eye.configurations_data USING gist (
  uuid_to_bytea(configurationID),
  validity
);
--
-- migrate JSON data from eye.configurations.configuration into new
-- eye.configurations_data table
ALTER TABLE eye.configurations_data ALTER COLUMN dataID SET DEFAULT gen_random_uuid();
INSERT INTO eye.configurations_data (configurationid, configuration) SELECT configurationid, configuration FROM eye.configurations;
ALTER TABLE eye.configurations_data ALTER COLUMN dataID DROP DEFAULT;
ALTER TABLE eye.configurations DROP COLUMN configuration;
--
-- create new table for eye data consumer registrations
CREATE TABLE IF NOT EXISTS eye.registry (
  registrationID          uuid            PRIMARY KEY,
  application             varchar(128)    NOT NULL,
  address                 inet            NOT NULL,
  port                    numeric(5,0)    NOT NULL CONSTRAINT valid_port CHECK ( port > 0 AND port < 65536 ),
  database                numeric(5,0)    NOT NULL CONSTRAINT valid_db CHECK ( database >= 0 ),
  registeredAt            timestamptz(3)  NOT NULL DEFAULT NOW(),
  CONSTRAINT registeredAt_utc CHECK( EXTRACT( TIMEZONE FROM registeredAt ) = '0' )
);
--
-- create new table eye.provisions which records the provisioning period
-- of configurations
CREATE TABLE IF NOT EXISTS eye.provisions (
  dataID                  uuid            NOT NULL,
  configurationID         uuid            NOT NULL,
  provision_period        tstzrange       NOT NULL DEFAULT tstzrange(NOW()::timestamptz(3), 'infinity', '[]'),
  tasks                   varchar(128)[]  NOT NULL,
  EXCLUDE USING gist (uuid_to_bytea(configurationID) WITH =, provision_period WITH &&),
  CONSTRAINT provisionedAt_utc CHECK( EXTRACT( TIMEZONE FROM lower( provision_period ) ) = '0' ),
  CONSTRAINT deprovisionedAt_utc CHECK( EXTRACT( TIMEZONE FROM upper( provision_period ) ) = '0' ),
  FOREIGN KEY ( dataID, configurationID ) REFERENCES eye.configurations_data( dataID, configurationID ) ON DELETE RESTRICT
);
--
-- create new table eye.activations which records at which point in time
-- a configuration was activated by a consumer
CREATE TABLE IF NOT EXISTS eye.activations (
  configurationID         uuid            NOT NULL REFERENCES eye.configurations( configurationID ) ON DELETE RESTRICT,
  activatedAt             timestamptz(3)  NOT NULL DEFAULT NOW(),
  CONSTRAINT activatedAt_utc CHECK( EXTRACT( TIMEZONE FROM activatedAt ) = '0' ),
  UNIQUE ( configurationID )
);
--
-- create schema version registry
CREATE TABLE IF NOT EXISTS public.schema_versions (
  serial                  bigserial       PRIMARY KEY,
  schema                  varchar(16)     NOT NULL,
  version                 numeric(16,0)   NOT NULL,
  created_at              timestamptz(3)  NOT NULL DEFAULT NOW()::timestamptz(3),
  description             text            NOT NULL
);
--
-- register schema version installation
INSERT INTO public.schema_versions (
  schema,
  version,
  description
) VALUES (
  'eye',
  201805070001,
  'Schema migration via: schema-upgrade.201607010001:201805070001.sql'
);
--
-- grant service user access to new tables
GRANT INSERT,SELECT,UPDATE,DELETE ON ALL TABLES IN SCHEMA eye TO eye_service;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO eye_service;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO eye_service;
