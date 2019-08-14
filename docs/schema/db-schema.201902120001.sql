-- SCHEMA VERSION: 201902120001
--
-- connect as RDBMS superuser
--
-- create roles for running eye

\connect postgres
CREATE ROLE eye_dba WITH NOSUPERUSER NOCREATEDB NOCREATEROLE LOGIN ENCRYPTED PASSWORD 'veryStrongAndSecretPassword';
CREATE ROLE eye_service WITH NOSUPERUSER NOCREATEDB NOCREATEROLE LOGIN ENCRYPTED PASSWORD 'similarlyStrongAndSecretPassword';
--
-- create database
CREATE DATABASE eye WITH OWNER eye_dba ENCODING 'UTF8' LC_COLLATE 'en_US.UTF-8' LC_CTYPE 'en_US.UTF-8' TEMPLATE template0;
GRANT CONNECT ON DATABASE eye TO eye_dba;
GRANT CONNECT ON DATABASE eye TO eye_service;
--
-- install extensions in eye database
\connect eye
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
--
-- reconnect as eye_dba user (DB Owner)
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
-- setup schema eye
CREATE SCHEMA IF NOT EXISTS eye;
SET search_path TO eye;
ALTER DATABASE eye SET search_path TO eye;
--
-- create table lookup
CREATE TABLE IF NOT EXISTS eye.lookup (
  lookupID                char(64)        PRIMARY KEY,
  hostID                  numeric(16,0)   NOT NULL,
  hostname                text            NOT NULL,
  metric                  text            NOT NULL
);
--
-- create table configurations
CREATE TABLE IF NOT EXISTS eye.configurations (
  configurationID         uuid            PRIMARY KEY,
  lookupID                char(64)        NOT NULL REFERENCES eye.lookup( lookupID )
);
--
-- create lookup acceleration index
CREATE INDEX _configurations_lookup ON eye.configurations (
  lookupID,
  configurationID
);
--
-- create table configurations_data
CREATE TABLE IF NOT EXISTS eye.configurations_data (
  dataID                  uuid            PRIMARY KEY,
  configurationID         uuid            NOT NULL REFERENCES eye.configurations( configurationID ) ON DELETE RESTRICT,
  validity                tstzrange       NOT NULL DEFAULT tstzrange(NOW()::timestamptz(3), 'infinity', '[]'),
  configuration           jsonb           NOT NULL,
  EXCLUDE USING gist (uuid_to_bytea(configurationID) WITH =, validity WITH &&),
  CONSTRAINT validFrom_utc CHECK( EXTRACT( TIMEZONE FROM lower( validity ) ) = '0' ),
  CONSTRAINT validUntil_utc CHECK( EXTRACT( TIMEZONE FROM upper( validity ) ) = '0' )
);
--
-- create unique index that is required to define a foreign key
-- referencing these two columns
CREATE UNIQUE INDEX _configuration_data ON eye.configurations_data (
  dataID,
  configurationID
);
--
-- create gist index to accelerate range queries
CREATE INDEX _configurations_data_range_query ON eye.configurations_data USING gist (
  uuid_to_bytea(configurationID),
  validity
);
--
-- registry records active applications using EYE
CREATE TABLE IF NOT EXISTS eye.registry (
  registrationID          uuid            PRIMARY KEY,
  application             varchar(128)    NOT NULL,
  address                 inet            NOT NULL,
  port                    numeric(5,0)    NOT NULL CONSTRAINT valid_port CHECK ( port > 0 AND port < 65536 ),
  database                numeric(5,0)    NOT NULL CONSTRAINT valid_db CHECK ( database >= 0 ),
  registeredAt            timestamptz(3)  NOT NULL DEFAULT NOW(),
  CONSTRAINT registeredAt_utc CHECK( EXTRACT( TIMEZONE FROM registeredAt ) = '0' ),
  UNIQUE( application, address, port, database )
);
--
-- provisioning records when a profile is rolled out
CREATE TABLE IF NOT EXISTS eye.provisions (
  dataID                  uuid            NOT NULL,
  configurationID         uuid            NOT NULL,
  provision_period        tstzrange       NOT NULL DEFAULT tstzrange(NOW()::timestamptz(3), 'infinity', '[]'),
  tasks                   varchar(128)[]  NOT NULL,
  EXCLUDE USING gist (uuid_to_bytea(configurationID) WITH =, provision_period WITH &&) DEFERRABLE,
  CONSTRAINT provisionedAt_utc CHECK( EXTRACT( TIMEZONE FROM lower( provision_period ) ) = '0' ),
  CONSTRAINT deprovisionedAt_utc CHECK( EXTRACT( TIMEZONE FROM upper( provision_period ) ) = '0' ),
  FOREIGN KEY ( dataID, configurationID ) REFERENCES eye.configurations_data( dataID, configurationID ) ON DELETE RESTRICT
);
--
-- activations records when a profile becomes active, ie. metrics for it
-- are received
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
  201902120001,
  'Initial setup via: db-schema.201902120001.sql'
);
--
-- allow service account to use the database
GRANT INSERT, SELECT, UPDATE, DELETE ON ALL TABLES IN SCHEMA eye TO eye_service;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO eye_service;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO eye_service;
GRANT USAGE ON SCHEMA eye TO eye_service;
