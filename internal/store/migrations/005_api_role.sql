-- Read-only role for the public API. The collector sets its password and
-- enables login from API_DB_PASSWORD at startup; without that it stays NOLOGIN.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'susq_api') THEN
        CREATE ROLE susq_api NOLOGIN CONNECTION LIMIT 20;
    END IF;
END
$$;

ALTER ROLE susq_api SET default_transaction_read_only = on;
ALTER ROLE susq_api SET statement_timeout = '15s';
ALTER ROLE susq_api SET idle_in_transaction_session_timeout = '30s';

GRANT USAGE ON SCHEMA public TO susq_api;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO susq_api;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO susq_api;
