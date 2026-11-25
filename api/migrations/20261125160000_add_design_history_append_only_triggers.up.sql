-- Design history is append-only (change design-history D3). The only way past
-- these triggers is the retention purge, which sets, inside its own transaction:
--   SELECT set_config('design.retention', 'on', true);
-- This protects against application bugs and compromised application paths; it
-- does NOT protect against someone who can ALTER TABLE ... DISABLE TRIGGER.

CREATE FUNCTION design_guard_retention() RETURNS boolean LANGUAGE sql STABLE AS
$$ SELECT coalesce(current_setting('design.retention', true), '') = 'on' $$;

-- design_request_events: no UPDATE, no DELETE.
CREATE FUNCTION design_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF design_guard_retention() THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'design_request_events is append-only (% refused)', TG_OP USING ERRCODE = 'restrict_violation';
END $$;
CREATE TRIGGER design_request_events_append_only
  BEFORE UPDATE OR DELETE ON design_request_events
  FOR EACH ROW EXECUTE FUNCTION design_events_append_only();

-- design_screenshots: no DELETE; UPDATE only of purged_at.
CREATE FUNCTION design_screenshots_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF design_guard_retention() THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
  END IF;
  IF TG_OP = 'UPDATE' AND (to_jsonb(NEW) - 'purged_at') = (to_jsonb(OLD) - 'purged_at') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'design_screenshots is append-only (% refused; only purged_at may change)', TG_OP USING ERRCODE = 'restrict_violation';
END $$;
CREATE TRIGGER design_screenshots_guard
  BEFORE UPDATE OR DELETE ON design_screenshots
  FOR EACH ROW EXECUTE FUNCTION design_screenshots_guard();

-- design_untracked_changes: no DELETE; UPDATE only of the acknowledgement, once.
CREATE FUNCTION design_untracked_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF design_guard_retention() THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
  END IF;
  IF TG_OP = 'UPDATE'
     AND OLD.acknowledged_at IS NULL
     AND (to_jsonb(NEW) - 'acknowledged_by_user_id' - 'acknowledged_at')
       = (to_jsonb(OLD) - 'acknowledged_by_user_id' - 'acknowledged_at') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'design_untracked_changes is append-only (% refused; the acknowledgement can be set once)', TG_OP USING ERRCODE = 'restrict_violation';
END $$;
CREATE TRIGGER design_untracked_changes_guard
  BEFORE UPDATE OR DELETE ON design_untracked_changes
  FOR EACH ROW EXECUTE FUNCTION design_untracked_guard();

-- design_requests: identity columns never change; rows are deleted only by retention.
CREATE FUNCTION design_requests_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF design_guard_retention() THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
  END IF;
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'design_requests rows are deleted only by the retention purge' USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.shop_id IS DISTINCT FROM OLD.shop_id
     OR NEW.kind IS DISTINCT FROM OLD.kind
     OR NEW.site IS DISTINCT FROM OLD.site
     OR NEW.requested_by_user_id IS DISTINCT FROM OLD.requested_by_user_id
     OR NEW.prompt_sha256 IS DISTINCT FROM OLD.prompt_sha256
     OR NEW.base_sha IS DISTINCT FROM OLD.base_sha
     OR NEW.created_at IS DISTINCT FROM OLD.created_at
     OR NEW.restores_request_id IS DISTINCT FROM OLD.restores_request_id THEN
    RAISE EXCEPTION 'design_requests: immutable column changed' USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER design_requests_guard
  BEFORE UPDATE OR DELETE ON design_requests
  FOR EACH ROW EXECUTE FUNCTION design_requests_guard();
