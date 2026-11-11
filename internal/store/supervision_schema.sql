ALTER TABLE bead_tasks ADD COLUMN responsible_agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE bead_tasks ADD COLUMN responsibility_epoch BIGINT NOT NULL DEFAULT 1;
ALTER TABLE bead_tasks ADD COLUMN progress_seq BIGINT NOT NULL DEFAULT 1;
ALTER TABLE bead_tasks ADD COLUMN progress_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint;
ALTER TABLE bead_tasks ADD COLUMN completion_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint;
ALTER TABLE bead_tasks ADD COLUMN result_run_id BIGINT REFERENCES agent_runs(id);
ALTER TABLE bead_tasks ADD COLUMN result_ready_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bead_tasks ADD COLUMN result_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE bead_comments ADD COLUMN run_id BIGINT REFERENCES agent_runs(id);

-- Logical responsibility survives a runtime. Manager identity comes from the
-- authoritative registered ancestry, including nested workstreams.
CREATE FUNCTION commander_supervisor(p BIGINT,b TEXT) RETURNS TEXT LANGUAGE sql STABLE AS $$
 SELECT commander_planning_owner(p,b)
$$;

CREATE TABLE bead_supervision (
 project_id BIGINT NOT NULL,bead_id TEXT NOT NULL,
 epoch BIGINT NOT NULL,progress_seq BIGINT NOT NULL,
 request_id TEXT NOT NULL DEFAULT gen_random_uuid()::text,
 reason TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'observing',
 target_agent_id TEXT NOT NULL DEFAULT '',
 opened_at BIGINT NOT NULL,phase_since BIGINT NOT NULL,
 due_at BIGINT NOT NULL,attempts BIGINT NOT NULL DEFAULT 0,
 delivered_at BIGINT NOT NULL DEFAULT 0,acknowledged_at BIGINT NOT NULL DEFAULT 0,
 lease_until BIGINT NOT NULL DEFAULT 0,lease_token TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '',escalation_level BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(project_id,bead_id),
 FOREIGN KEY(project_id,bead_id) REFERENCES bead_tasks(project_id,id)
);
CREATE INDEX bead_supervision_due ON bead_supervision(due_at,project_id,bead_id);
CREATE TABLE supervision_attempts (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id BIGINT NOT NULL,bead_id TEXT NOT NULL,epoch BIGINT NOT NULL,
 target_agent_id TEXT NOT NULL,reason TEXT NOT NULL,kind TEXT NOT NULL,
 occurred_at BIGINT NOT NULL,detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX supervision_attempts_bead ON supervision_attempts(project_id,bead_id,occurred_at);

CREATE FUNCTION commander_track_responsibility() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE actor TEXT;old_planner TEXT;new_planner TEXT;
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.responsible_agent_id='' THEN NEW.responsible_agent_id=commander_supervisor(NEW.project_id,NEW.parent_id); END IF;
  RETURN NEW;
 END IF;
 IF NEW.parent_id IS DISTINCT FROM OLD.parent_id THEN
  SELECT COALESCE((SELECT commander_id FROM commander_jurisdictions WHERE project_id=OLD.project_id AND bead_id=OLD.id),commander_planning_owner(OLD.project_id,OLD.parent_id)) INTO old_planner;
  SELECT COALESCE((SELECT commander_id FROM commander_jurisdictions WHERE project_id=NEW.project_id AND bead_id=NEW.id),commander_planning_owner(NEW.project_id,NEW.parent_id)) INTO new_planner;
  IF NEW.responsible_agent_id=old_planner THEN NEW.responsible_agent_id=new_planner; END IF;
 END IF;
 IF NEW.active_run_id IS DISTINCT FROM OLD.active_run_id AND NEW.active_run_id IS NOT NULL THEN
  SELECT agent_name INTO actor FROM agent_runs WHERE id=NEW.active_run_id;
  NEW.responsible_agent_id=COALESCE(actor,NEW.responsible_agent_id);
  NEW.result_run_id=NULL;NEW.result_ready_at=0;NEW.result_summary='';
 END IF;
 IF NEW.responsible_agent_id IS DISTINCT FROM OLD.responsible_agent_id OR NEW.active_run_id IS DISTINCT FROM OLD.active_run_id OR NEW.parent_id IS DISTINCT FROM OLD.parent_id THEN
  NEW.responsibility_epoch=OLD.responsibility_epoch+1;
 END IF;
 -- Heartbeats, chat text and cosmetic edits never renew the progress clock.
 IF NEW.status IS DISTINCT FROM OLD.status OR NEW.result_run_id IS DISTINCT FROM OLD.result_run_id OR NEW.active_run_id IS DISTINCT FROM OLD.active_run_id OR NEW.execution_ready IS DISTINCT FROM OLD.execution_ready THEN
  IF NEW.status='closed' THEN NEW.completion_at=EXTRACT(EPOCH FROM clock_timestamp())::bigint; END IF;
  NEW.progress_seq=OLD.progress_seq+1;NEW.progress_at=EXTRACT(EPOCH FROM clock_timestamp())::bigint;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bead_responsibility BEFORE INSERT OR UPDATE ON bead_tasks FOR EACH ROW EXECUTE FUNCTION commander_track_responsibility();

CREATE FUNCTION commander_report_result(p BIGINT,b TEXT,rid BIGINT,session TEXT,summary TEXT) RETURNS BOOLEAN LANGUAGE plpgsql AS $$
DECLARE current_run BIGINT;current_session TEXT;
BEGIN
 SELECT active_run_id INTO current_run FROM bead_tasks WHERE project_id=p AND id=b AND deleted_at IS NULL FOR UPDATE;
 IF current_run IS DISTINCT FROM rid THEN RETURN FALSE; END IF;
 SELECT opencode_session_id INTO current_session FROM agent_runs WHERE id=rid;
 IF current_session IS DISTINCT FROM session THEN RETURN FALSE; END IF;
 UPDATE bead_tasks SET result_run_id=rid,result_ready_at=EXTRACT(EPOCH FROM clock_timestamp())::bigint,
 result_summary=LEFT(summary,6000),responsible_agent_id=commander_supervisor(p,b),resume_run_id=NULL,resume_plan_revision=0,execution_ready=FALSE
 WHERE project_id=p AND id=b AND result_run_id IS DISTINCT FROM rid AND status<>'closed';
 RETURN TRUE;
END $$;

CREATE FUNCTION commander_seed_supervision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO bead_supervision(project_id,bead_id,epoch,progress_seq,opened_at,phase_since,due_at)
 VALUES(NEW.project_id,NEW.id,NEW.responsibility_epoch,NEW.progress_seq,NEW.progress_at,NEW.progress_at,0)
 ON CONFLICT(project_id,bead_id) DO UPDATE SET due_at=CASE WHEN (NEW.status='closed' AND NOT commander_pending_human_answer(NEW.project_id,NEW.id)) OR NEW.deleted_at IS NOT NULL THEN 9223372036854775807 ELSE 0 END,
 state=CASE WHEN (NEW.status='closed' AND NOT commander_pending_human_answer(NEW.project_id,NEW.id)) OR NEW.deleted_at IS NOT NULL THEN 'resolved' ELSE bead_supervision.state END,
 lease_until=CASE WHEN NEW.responsibility_epoch<>OLD.responsibility_epoch OR NEW.progress_seq<>OLD.progress_seq THEN 0 ELSE bead_supervision.lease_until END,
 lease_token=CASE WHEN NEW.responsibility_epoch<>OLD.responsibility_epoch OR NEW.progress_seq<>OLD.progress_seq THEN '' ELSE bead_supervision.lease_token END;
 RETURN NULL;
END $$;
CREATE TRIGGER bead_supervision_changed AFTER INSERT OR UPDATE ON bead_tasks FOR EACH ROW EXECUTE FUNCTION commander_seed_supervision();
CREATE TRIGGER bead_supervision_events AFTER INSERT OR DELETE ON bead_supervision FOR EACH ROW EXECUTE FUNCTION commander_state_event();
CREATE TRIGGER bead_supervision_update_events AFTER UPDATE ON bead_supervision FOR EACH ROW WHEN
 ((OLD.state,OLD.reason,OLD.target_agent_id,OLD.attempts,OLD.last_error,OLD.acknowledged_at) IS DISTINCT FROM (NEW.state,NEW.reason,NEW.target_agent_id,NEW.attempts,NEW.last_error,NEW.acknowledged_at)) EXECUTE FUNCTION commander_state_event();

UPDATE bead_tasks b SET responsible_agent_id=CASE WHEN r.status='running' THEN r.agent_name ELSE commander_supervisor(b.project_id,b.id) END FROM agent_runs r WHERE b.active_run_id=r.id;
UPDATE bead_tasks b SET responsible_agent_id=commander_supervisor(b.project_id,b.id) WHERE responsible_agent_id='';
INSERT INTO bead_supervision(project_id,bead_id,epoch,progress_seq,opened_at,phase_since,due_at)
SELECT project_id,id,responsibility_epoch,progress_seq,progress_at,progress_at,0 FROM bead_tasks ON CONFLICT DO NOTHING;

CREATE INDEX agent_state_normalized_role ON agent_state((replace(role,'.','-')),updated DESC);
CREATE TABLE supervision_dispatch (
 agent_id TEXT PRIMARY KEY,next_delivery_at BIGINT NOT NULL DEFAULT 0,
 lease_until BIGINT NOT NULL DEFAULT 0,lease_token TEXT NOT NULL DEFAULT ''
);

-- A subordinate's real phase/result transition advances its ancestors. Only
-- the immediate parent is updated; cascading follows each edge once.
CREATE FUNCTION commander_bubble_progress() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.parent_id IS NOT NULL AND (TG_OP='INSERT' OR NEW.progress_seq IS DISTINCT FROM OLD.progress_seq) AND pg_trigger_depth()<64 THEN
  UPDATE bead_tasks SET progress_seq=progress_seq+1,progress_at=NEW.progress_at,completion_at=CASE WHEN NEW.status='closed' OR (TG_OP='UPDATE' AND NEW.completion_at IS DISTINCT FROM OLD.completion_at) THEN GREATEST(completion_at,NEW.completion_at) ELSE completion_at END
  WHERE project_id=NEW.project_id AND id=NEW.parent_id AND status<>'closed';
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER bead_progress_parent AFTER INSERT OR UPDATE ON bead_tasks FOR EACH ROW EXECUTE FUNCTION commander_bubble_progress();

-- Guarded tree edits hold the same project advisory lock as scope transfers.
-- A move invalidates planning revisions and pending recovery in the moved
-- inherited scope, while preserving nested overrides and live executors.
CREATE FUNCTION commander_reparent_jurisdiction() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_planner TEXT;new_planner TEXT;
BEGIN
 IF NEW.parent_id IS NOT DISTINCT FROM OLD.parent_id THEN RETURN NULL; END IF;
 SELECT COALESCE((SELECT commander_id FROM commander_jurisdictions WHERE project_id=OLD.project_id AND bead_id=OLD.id),commander_planning_owner(OLD.project_id,OLD.parent_id)) INTO old_planner;
 new_planner=commander_planning_owner(NEW.project_id,NEW.id);
 INSERT INTO jurisdiction_projects(project_id,revision) VALUES(NEW.project_id,2) ON CONFLICT(project_id) DO UPDATE SET revision=jurisdiction_projects.revision+1;
 WITH RECURSIVE scope(id,path) AS (
  SELECT id,ARRAY[id] FROM bead_tasks WHERE project_id=NEW.project_id AND parent_id=NEW.id
   AND NOT EXISTS(SELECT 1 FROM commander_jurisdictions j WHERE j.project_id=NEW.project_id AND j.bead_id=bead_tasks.id)
  UNION ALL SELECT b.id,s.path||b.id FROM bead_tasks b JOIN scope s ON b.parent_id=s.id WHERE b.project_id=NEW.project_id AND NOT b.id=ANY(s.path)
   AND NOT EXISTS(SELECT 1 FROM commander_jurisdictions j WHERE j.project_id=b.project_id AND j.bead_id=b.id)
 ) UPDATE bead_tasks b SET responsible_agent_id=CASE WHEN b.responsible_agent_id=old_planner THEN new_planner ELSE b.responsible_agent_id END,
 responsibility_epoch=b.responsibility_epoch+1 WHERE b.project_id=NEW.project_id AND b.id IN(SELECT id FROM scope);
 RETURN NULL;
END $$;
CREATE TRIGGER bead_reparent_jurisdiction AFTER UPDATE OF parent_id ON bead_tasks FOR EACH ROW EXECUTE FUNCTION commander_reparent_jurisdiction();


-- User answers are durable obligations for the current planning owner even
-- when a crash or jurisdiction handoff interrupts their application.
CREATE FUNCTION commander_human_answer_progress() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.generation=(SELECT CAST(COALESCE(MAX(version),0) AS TEXT) FROM pitches WHERE project_id=NEW.project_id AND bead_id=NEW.bead_id) THEN
  UPDATE bead_tasks SET responsible_agent_id=commander_planning_owner(project_id,id),progress_seq=progress_seq+1,progress_at=EXTRACT(EPOCH FROM clock_timestamp())::bigint WHERE project_id=NEW.project_id AND id=NEW.bead_id AND deleted_at IS NULL;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER human_answer_progress AFTER INSERT OR UPDATE ON needs_resolutions FOR EACH ROW EXECUTE FUNCTION commander_human_answer_progress();
