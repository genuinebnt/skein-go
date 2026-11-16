-- One schema per service, each owned by its own login role. A service's role
-- can use only its own schema, which enforces data ownership (ADR-0001).
-- Local development passwords only; real credentials come from Secret Manager.
DO $$
DECLARE
  svc text;
BEGIN
  FOREACH svc IN ARRAY ARRAY['identity', 'documents', 'search', 'ml', 'agent', 'notifications'] LOOP
    EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', svc, svc);
    EXECUTE format('CREATE SCHEMA %I AUTHORIZATION %I', svc, svc);
    EXECUTE format('ALTER ROLE %I SET search_path = %I', svc, svc);
  END LOOP;
END
$$;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
