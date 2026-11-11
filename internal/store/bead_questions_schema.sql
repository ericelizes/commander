CREATE TABLE bead_questions (
 id TEXT PRIMARY KEY,
 project_id BIGINT NOT NULL,
 bead_id TEXT NOT NULL,
 kind TEXT NOT NULL DEFAULT 'execution' CHECK(kind IN('execution','planning')),
 canonical_bead_id TEXT GENERATED ALWAYS AS (NULLIF(bead_id,'')) STORED,
 CHECK(bead_id<>'' OR kind='planning'),
 plan_revision BIGINT NOT NULL,
 raised_by TEXT NOT NULL,
 client_key TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 run_id BIGINT REFERENCES agent_runs(id),
 request_run_id BIGINT GENERATED ALWAYS AS (COALESCE(run_id,0)) STORED,
 question TEXT NOT NULL,
 recommendation TEXT NOT NULL DEFAULT '',
 checkpoint TEXT NOT NULL DEFAULT '',
 checkpoint_runtime TEXT NOT NULL DEFAULT '',
 checkpoint_thread TEXT NOT NULL DEFAULT '',
 blocking BOOLEAN NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN('pending','answered','superseded')),
 created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint,
 answered_at BIGINT NOT NULL DEFAULT 0,
 FOREIGN KEY(project_id) REFERENCES projects(id),
 FOREIGN KEY(project_id,canonical_bead_id) REFERENCES bead_tasks(project_id,id),
 UNIQUE(project_id,bead_id,raised_by,client_key,plan_revision,request_run_id)
);
CREATE INDEX bead_questions_pending ON bead_questions(project_id,bead_id,state);
CREATE INDEX bead_questions_run ON bead_questions(run_id);
CREATE TABLE bead_question_scopes (
 question_id TEXT NOT NULL REFERENCES bead_questions(id),
 project_id BIGINT NOT NULL,
 bead_id TEXT NOT NULL,
 project_scope BOOLEAN NOT NULL DEFAULT FALSE,
 canonical_bead_id TEXT GENERATED ALWAYS AS (NULLIF(bead_id,'')) STORED,
 requested_jurisdiction_revision BIGINT NOT NULL DEFAULT 0,
 delivery_owner TEXT NOT NULL DEFAULT '',
 delivery_revision BIGINT NOT NULL DEFAULT 0,
 delivery_attempts BIGINT NOT NULL DEFAULT 0,
 delivery_due_at BIGINT NOT NULL DEFAULT 0,
 delivery_lease_until BIGINT NOT NULL DEFAULT 0,
 delivery_token TEXT NOT NULL DEFAULT '',
 delivery_error TEXT NOT NULL DEFAULT '',
 CHECK((project_scope AND bead_id='') OR (NOT project_scope AND bead_id<>'')),
 plan_revision BIGINT NOT NULL,
 PRIMARY KEY(question_id,project_id,bead_id),
 FOREIGN KEY(project_id) REFERENCES projects(id),
 FOREIGN KEY(project_id,canonical_bead_id) REFERENCES bead_tasks(project_id,id)
);
CREATE INDEX bead_question_scopes_bead ON bead_question_scopes(project_id,bead_id);
CREATE TABLE bead_question_answers (
 question_id TEXT NOT NULL,
 project_id BIGINT NOT NULL,
 bead_id TEXT NOT NULL,
 jurisdiction_revision BIGINT NOT NULL,
 actor TEXT NOT NULL,
 answer TEXT NOT NULL,
 approved BOOLEAN NOT NULL,
 created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint,
 PRIMARY KEY(question_id,project_id,bead_id,jurisdiction_revision),
 FOREIGN KEY(question_id,project_id,bead_id) REFERENCES bead_question_scopes(question_id,project_id,bead_id)
);
CREATE TRIGGER bead_questions_events AFTER INSERT OR UPDATE ON bead_questions FOR EACH ROW EXECUTE FUNCTION commander_state_event();
CREATE TRIGGER bead_question_scopes_events AFTER INSERT ON bead_question_scopes FOR EACH ROW EXECUTE FUNCTION commander_state_event();
CREATE TRIGGER bead_question_answers_events AFTER INSERT ON bead_question_answers FOR EACH ROW EXECUTE FUNCTION commander_state_event();

CREATE FUNCTION commander_question_due() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE bead_supervision SET due_at=0 WHERE project_id=NEW.project_id AND bead_id=NEW.bead_id;
 RETURN NULL;
END $$;
CREATE TRIGGER bead_question_due AFTER INSERT OR UPDATE ON bead_questions FOR EACH ROW EXECUTE FUNCTION commander_question_due();
CREATE TRIGGER bead_question_scope_due AFTER INSERT ON bead_question_scopes FOR EACH ROW EXECUTE FUNCTION commander_question_due();

CREATE FUNCTION commander_question_plan_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.plan_revision IS DISTINCT FROM OLD.plan_revision OR NEW.status='closed' OR NEW.deleted_at IS NOT NULL THEN
  UPDATE bead_questions q SET state='superseded' FROM bead_question_scopes s WHERE s.question_id=q.id AND s.project_id=NEW.project_id AND s.bead_id=NEW.id AND q.state='pending' AND (s.plan_revision<>NEW.plan_revision OR (NEW.status='closed' AND q.kind='execution') OR NEW.deleted_at IS NOT NULL);
 END IF;
 IF NEW.active_run_id IS DISTINCT FROM OLD.active_run_id THEN
  UPDATE bead_questions SET state='superseded' WHERE project_id=NEW.project_id AND bead_id=NEW.id AND state='pending' AND run_id IS NOT NULL AND run_id IS DISTINCT FROM NEW.active_run_id;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER bead_question_plan_changed AFTER UPDATE ON bead_tasks FOR EACH ROW EXECUTE FUNCTION commander_question_plan_changed();

CREATE FUNCTION commander_resume_question_run(p BIGINT,b TEXT) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 UPDATE bead_tasks t SET execution_ready=TRUE,status='open'
 WHERE t.project_id=p AND t.id=b AND t.resume_run_id=t.active_run_id AND t.resume_plan_revision=t.plan_revision AND t.status IN('open','in_progress')
 AND btrim(t.description)<>'' AND btrim(t.acceptance_criteria)<>''
 AND EXISTS(SELECT 1 FROM agent_runs r WHERE r.id=t.resume_run_id AND r.status IN('exited','closed','orphan-reaped','supervision-missing','native-error'))
 AND EXISTS(SELECT 1 FROM bead_questions q WHERE q.run_id=t.resume_run_id AND q.blocking AND q.state='answered')
 AND NOT EXISTS(SELECT 1 FROM bead_questions q WHERE q.run_id=t.resume_run_id AND q.blocking AND q.state<>'answered');
END $$;

-- Planning proposals bind the project ownership version. Ordinary execution
-- questions instead follow a handoff to their current planning owner.
CREATE FUNCTION commander_planning_question_jurisdiction_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE bead_questions q SET state='superseded' FROM bead_question_scopes sc
 WHERE sc.question_id=q.id AND sc.project_id=NEW.project_id AND q.kind='planning'
 AND q.state='pending' AND sc.requested_jurisdiction_revision<>NEW.revision;
 RETURN NULL;
END $$;
CREATE TRIGGER planning_question_jurisdiction_changed AFTER INSERT OR UPDATE OF revision,default_commander ON jurisdiction_projects FOR EACH ROW EXECUTE FUNCTION commander_planning_question_jurisdiction_changed();
CREATE INDEX planning_question_delivery_due ON bead_question_scopes(delivery_due_at,project_id,question_id);
