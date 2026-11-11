-- Typed checker evidence extends the canonical bead comment history. It is
-- not a second task store and cannot be supplied by the public comment door.
ALTER TABLE bead_comments ADD COLUMN fact_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE bead_comments ADD COLUMN fact_definition TEXT NOT NULL DEFAULT '';
ALTER TABLE bead_comments ADD COLUMN fact_value JSONB NOT NULL DEFAULT '{}';
CREATE INDEX bead_comment_facts ON bead_comments(project_id,bead_id,fact_kind,created_at DESC,id DESC) WHERE fact_kind<>'';

-- Executable intent belongs to the canonical bead, not a parallel task queue.
CREATE TABLE orchestration_control (
 id BIGINT PRIMARY KEY CHECK(id=1),
 mode TEXT NOT NULL CHECK(mode IN('off','shadow','active')),
 revision BIGINT NOT NULL DEFAULT 1,
 reason TEXT NOT NULL DEFAULT '',
 updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint
);
INSERT INTO orchestration_control(id,mode) VALUES(1,'shadow');
CREATE TRIGGER orchestration_control_events AFTER UPDATE ON orchestration_control FOR EACH ROW EXECUTE FUNCTION commander_state_event();

ALTER TABLE bead_tasks ADD COLUMN plan_revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE bead_tasks ADD COLUMN execution_ready BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE bead_tasks ADD COLUMN execution_capabilities JSONB NOT NULL DEFAULT '[]';
ALTER TABLE bead_tasks ADD COLUMN execution_attempts BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bead_tasks ADD COLUMN execution_due_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bead_tasks ADD COLUMN execution_error TEXT NOT NULL DEFAULT '';
ALTER TABLE bead_tasks ADD COLUMN resume_run_id BIGINT REFERENCES agent_runs(id);
ALTER TABLE bead_tasks ADD COLUMN resume_plan_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bead_tasks ADD COLUMN human_authority_epoch BIGINT NOT NULL DEFAULT 1;
ALTER TABLE bead_tasks ADD COLUMN human_scope_epoch BIGINT NOT NULL DEFAULT 1;
ALTER TABLE bead_tasks ADD CONSTRAINT bead_ready_brief CHECK (NOT execution_ready OR (btrim(description)<>'' AND btrim(acceptance_criteria)<>''));
CREATE INDEX bead_execution_ready ON bead_tasks(priority,execution_due_at,project_id,id) WHERE execution_ready AND status='open' AND deleted_at IS NULL;

-- Slots are runtime capacity, not planning ownership. A slot can execute one
-- bead at a time; the resulting run remains the canonical execution identity.
CREATE TABLE execution_slots (
 agent_id TEXT PRIMARY KEY,
 capabilities JSONB NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 project_id BIGINT,
 bead_id TEXT,
 plan_revision BIGINT NOT NULL DEFAULT 0,
 responsibility_epoch BIGINT NOT NULL DEFAULT 0,
 workstream_capacity BIGINT NOT NULL DEFAULT 8,
 claim_token TEXT NOT NULL DEFAULT '',
 lease_until BIGINT NOT NULL DEFAULT 0,
 run_id BIGINT REFERENCES agent_runs(id),
 resume_run_id BIGINT REFERENCES agent_runs(id),
 FOREIGN KEY(project_id,bead_id) REFERENCES bead_tasks(project_id,id),
 CHECK ((project_id IS NULL)=(bead_id IS NULL))
);
CREATE UNIQUE INDEX execution_slots_bead ON execution_slots(project_id,bead_id) WHERE project_id IS NOT NULL;
CREATE UNIQUE INDEX execution_slots_token ON execution_slots(claim_token) WHERE claim_token<>'';
CREATE TRIGGER execution_slots_events AFTER INSERT OR DELETE ON execution_slots FOR EACH ROW EXECUTE FUNCTION commander_state_event();
CREATE TRIGGER execution_slots_update_events AFTER UPDATE ON execution_slots FOR EACH ROW WHEN
 ((OLD.capabilities,OLD.enabled,OLD.project_id,OLD.bead_id,OLD.claim_token,OLD.run_id) IS DISTINCT FROM
  (NEW.capabilities,NEW.enabled,NEW.project_id,NEW.bead_id,NEW.claim_token,NEW.run_id)) EXECUTE FUNCTION commander_state_event();

CREATE FUNCTION commander_execution_priority(p BIGINT,b TEXT) RETURNS BIGINT[] LANGUAGE sql STABLE AS $$
 WITH RECURSIVE ancestry(id,parent_id,depth,path) AS (
  SELECT id,parent_id,0,ARRAY[id] FROM bead_tasks WHERE project_id=p AND id=b
  UNION ALL SELECT t.id,t.parent_id,a.depth+1,a.path||t.id FROM bead_tasks t JOIN ancestry a ON a.parent_id=t.id WHERE t.project_id=p AND NOT t.id=ANY(a.path)
 ) SELECT array_agg(COALESCE(NULLIF(r.rank,0),NULLIF(w.rank,0),1000000) ORDER BY a.depth DESC)
 FROM ancestry a LEFT JOIN bead_ranks r ON r.project_id=p AND r.bead_id=a.id LEFT JOIN workstream_ranks w ON w.project_id=p AND w.bead_id=a.id
$$;

CREATE FUNCTION commander_plan_labels(labels JSONB) RETURNS JSONB LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(jsonb_agg(label ORDER BY label),'[]') FROM jsonb_array_elements_text(labels) label
 WHERE label !~ '^(cmdr:|owner:|wait-checked:|handoff-notified:)'
$$;

CREATE FUNCTION commander_track_plan() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='closed' OR NEW.deleted_at IS NOT NULL THEN NEW.execution_ready=FALSE; END IF;
 IF (NEW.title,NEW.description,NEW.acceptance_criteria,NEW.priority,NEW.parent_id,commander_plan_labels(NEW.labels),NEW.execution_capabilities,NEW.deleted_at,CASE WHEN NEW.status IN('closed','deferred') THEN NEW.status ELSE '' END)
 IS DISTINCT FROM (OLD.title,OLD.description,OLD.acceptance_criteria,OLD.priority,OLD.parent_id,commander_plan_labels(OLD.labels),OLD.execution_capabilities,OLD.deleted_at,CASE WHEN OLD.status IN('closed','deferred') THEN OLD.status ELSE '' END) THEN
  NEW.plan_revision=OLD.plan_revision+1;
 END IF;
 IF NEW.plan_revision IS DISTINCT FROM OLD.plan_revision THEN NEW.resume_run_id=NULL;NEW.resume_plan_revision=0; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bead_plan_revision BEFORE UPDATE ON bead_tasks FOR EACH ROW EXECUTE FUNCTION commander_track_plan();

-- Readiness is checked both at reservation and at binding a launched run.
-- An ancestor hold, typed wait or user gate cannot be bypassed by a leaf claim.
CREATE FUNCTION commander_human_restrictions(labels JSONB) RETURNS JSONB LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(jsonb_agg(label ORDER BY label),'[]') FROM jsonb_array_elements_text(labels) label
 WHERE label IN('hold','needs-user','gate','kind:gate') OR label ~ '^(needs-|approver:|verifier:)' OR label ~ '^waiting:[^:]+:user(:|$)' OR label ~ '^w:[^:]+:us(:|$)'
$$;
CREATE FUNCTION commander_authority_plan_labels(labels JSONB) RETURNS JSONB LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(jsonb_agg(label ORDER BY label),'[]') FROM jsonb_array_elements_text(commander_plan_labels(labels)) label
 WHERE label NOT IN('hold','needs-user','blocked','decision:answered','review','gate-status:failed') AND label !~ '^(needs-|waiting:|w:)'
$$;

CREATE FUNCTION commander_dependency_complete(state TEXT,labels JSONB) RETURNS BOOLEAN LANGUAGE sql IMMUTABLE AS $$
 SELECT state='closed' OR ((state='deferred' OR jsonb_exists_any(labels,ARRAY['retired','superseded'])) AND commander_human_restrictions(labels)='[]' AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(labels) l WHERE l LIKE 'workflow-template:%' OR l LIKE 'workflow-step:%'))
$$;

CREATE FUNCTION commander_pending_human_answer(p BIGINT,b TEXT) RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM needs_resolutions r WHERE r.project_id=p AND r.bead_id=b AND r.state IN(0,1) AND r.generation=(SELECT CAST(COALESCE(MAX(version),0) AS TEXT) FROM pitches WHERE project_id=p AND bead_id=b))
$$;

CREATE FUNCTION commander_execution_allowed(p BIGINT,b TEXT,resume_existing BOOLEAN DEFAULT FALSE) RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
 WITH RECURSIVE ancestry(id,parent_id,status,labels,path) AS (
  SELECT id,parent_id,status,labels,ARRAY[id] FROM bead_tasks WHERE project_id=p AND id=b AND deleted_at IS NULL
  UNION ALL SELECT t.id,t.parent_id,t.status,t.labels,a.path||t.id FROM bead_tasks t JOIN ancestry a ON a.parent_id=t.id
   WHERE t.project_id=p AND t.deleted_at IS NULL AND NOT t.id=ANY(a.path)
 ) SELECT EXISTS(SELECT 1 FROM ancestry WHERE id=b AND (status='open' OR (resume_existing AND status='in_progress')))
 AND NOT EXISTS(SELECT 1 FROM ancestry WHERE status IN('closed','deferred') OR jsonb_exists_any(labels,ARRAY['hold','needs-user','gate','kind:gate','superseded','retired']))
 AND NOT EXISTS(SELECT 1 FROM ancestry WHERE parent_id=ANY(path))
 AND NOT EXISTS(SELECT 1 FROM ancestry a WHERE commander_pending_human_answer(p,a.id))
 AND NOT EXISTS(SELECT 1 FROM bead_questions q JOIN ancestry a ON a.id=q.bead_id WHERE q.project_id=p AND q.blocking AND q.state='pending')
 AND NOT EXISTS(SELECT 1 FROM ancestry a,LATERAL jsonb_array_elements_text(a.labels) label
  WHERE (label LIKE 'waiting:%' OR label LIKE 'w:%') AND label !~ '^(waiting:[0-9]+:until:[0-9]+|w:[a-z0-9]+:un:[a-z0-9]+):Manager_20(to_20(close|plan|recover|start)|dispatch)$')
 AND NOT EXISTS(SELECT 1 FROM ancestry a JOIN bead_dependencies d ON d.project_id=p AND d.issue_id=a.id AND d.type='blocks'
  JOIN bead_tasks t ON t.project_id=p AND t.id=d.depends_on_id WHERE (NOT commander_dependency_complete(t.status,t.labels) OR commander_pending_human_answer(t.project_id,t.id)) AND t.deleted_at IS NULL
  AND NOT EXISTS(SELECT 1 FROM ancestry own WHERE own.id=t.id))
$$;

-- Capacity includes startup reservations as well as real runs. Checking only
-- live processes lets concurrent slots all pass a cap before any process starts.
CREATE FUNCTION commander_execution_workstream(p BIGINT,b TEXT) RETURNS TEXT LANGUAGE sql STABLE AS $$
 WITH RECURSIVE ancestry(id,parent_id,labels,depth,path) AS (
  SELECT id,parent_id,labels,0,ARRAY[id] FROM bead_tasks WHERE project_id=p AND id=b
  UNION ALL SELECT t.id,t.parent_id,t.labels,a.depth+1,a.path||t.id FROM bead_tasks t JOIN ancestry a ON a.parent_id=t.id WHERE t.project_id=p AND NOT t.id=ANY(a.path)
 ) SELECT COALESCE((SELECT id FROM ancestry WHERE jsonb_exists(labels,'kind:workstream') ORDER BY depth LIMIT 1),(SELECT id FROM ancestry ORDER BY depth DESC LIMIT 1))
$$;

CREATE FUNCTION commander_execution_capacity_allowed(p BIGINT,b TEXT,default_cap BIGINT,now_at BIGINT,exclude_slot TEXT) RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
 WITH RECURSIVE ancestry(id,parent_id,labels,depth,path) AS (
  SELECT id,parent_id,labels,0,ARRAY[id] FROM bead_tasks WHERE project_id=p AND id=b
  UNION ALL SELECT t.id,t.parent_id,t.labels,a.depth+1,a.path||t.id FROM bead_tasks t JOIN ancestry a ON a.parent_id=t.id WHERE t.project_id=p AND NOT t.id=ANY(a.path)
 ), limits AS (
  SELECT a.id,COALESCE(explicit.n,
   CASE WHEN a.parent_id IS NULL OR a.id=(SELECT id FROM ancestry WHERE jsonb_exists(labels,'kind:workstream') ORDER BY depth LIMIT 1) THEN default_cap ELSE 0 END) cap,
   explicit.n IS NOT NULL OR a.parent_id IS NULL AS subtree
  FROM ancestry a LEFT JOIN LATERAL (SELECT MIN(n) n FROM (SELECT CASE WHEN label ~ '^max-operators:[0-9]{1,9}$' THEN split_part(label,':',2)::bigint ELSE 0 END n FROM jsonb_array_elements_text(a.labels) label) caps WHERE n>0) explicit ON TRUE
 ), seeds(key,id,parent_id,path,owner_scope) AS (
  SELECT 'run:'||r.id,t.id,t.parent_id,ARRAY[t.id],commander_execution_workstream(p,t.id) FROM agent_runs r JOIN bead_tasks t ON t.project_id=r.project_id AND t.id=r.task_bead_id WHERE r.project_id=p AND r.status='running'
  UNION ALL SELECT 'slot:'||s.agent_id,t.id,t.parent_id,ARRAY[t.id],commander_execution_workstream(p,t.id) FROM execution_slots s JOIN bead_tasks t ON t.project_id=s.project_id AND t.id=s.bead_id WHERE s.project_id=p AND s.run_id IS NULL AND s.lease_until>now_at AND s.agent_id<>exclude_slot
 ), claimed(key,id,parent_id,path,owner_scope) AS (
  SELECT * FROM seeds
  UNION ALL SELECT c.key,t.id,t.parent_id,c.path||t.id,c.owner_scope FROM claimed c JOIN bead_tasks t ON t.id=c.parent_id AND t.project_id=p WHERE NOT t.id=ANY(c.path)
 ) SELECT NOT EXISTS(SELECT 1 FROM limits l WHERE l.cap>0 AND (SELECT COUNT(DISTINCT c.key) FROM claimed c WHERE c.id=l.id AND (l.subtree OR c.owner_scope=l.id))>=l.cap)
$$;

-- Dependency edits change planning intent even when no bead text changes.
-- Both endpoints are read dependencies of a batch; parent edges are already
-- versioned by the guarded parent_id mutation.
CREATE FUNCTION commander_dependency_plan_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p BIGINT; a TEXT; b TEXT;
BEGIN
 IF TG_OP='DELETE' THEN p=OLD.project_id;a=OLD.issue_id;b=OLD.depends_on_id;
  IF OLD.type='parent-child' THEN RETURN NULL; END IF;
 ELSE p=NEW.project_id;a=NEW.issue_id;b=NEW.depends_on_id;
  IF NEW.type='parent-child' THEN RETURN NULL; END IF;
 END IF;
 UPDATE bead_tasks SET plan_revision=plan_revision+1 WHERE project_id=p AND id IN(a,b);
 RETURN NULL;
END $$;
CREATE TRIGGER bead_dependency_plan_revision AFTER INSERT OR DELETE ON bead_dependencies FOR EACH ROW EXECUTE FUNCTION commander_dependency_plan_changed();

-- Adding, moving or removing a child changes the parent's planning structure.
CREATE FUNCTION commander_parent_plan_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  UPDATE bead_tasks SET plan_revision=plan_revision+1 WHERE project_id=NEW.project_id AND id=NEW.parent_id;
 ELSIF TG_OP='DELETE' THEN
  UPDATE bead_tasks SET plan_revision=plan_revision+1 WHERE project_id=OLD.project_id AND id=OLD.parent_id;
 ELSIF NEW.parent_id IS DISTINCT FROM OLD.parent_id OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
  UPDATE bead_tasks SET plan_revision=plan_revision+1 WHERE project_id=NEW.project_id AND id IN(OLD.parent_id,NEW.parent_id);
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER bead_parent_plan_revision AFTER INSERT OR UPDATE OR DELETE ON bead_tasks FOR EACH ROW EXECUTE FUNCTION commander_parent_plan_changed();

-- Commit receipts live in the same transaction as their canonical bead edits.
CREATE TABLE commander_plan_receipts (
 project_id BIGINT NOT NULL REFERENCES projects(id),
 actor TEXT NOT NULL,
 request_key TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 response JSONB NOT NULL,
 created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint,
 PRIMARY KEY(project_id,actor,request_key)
);

-- A proposal authorizes only the exact saved plan, never general scope access.
CREATE TABLE commander_plan_proposals (
 id TEXT PRIMARY KEY REFERENCES bead_questions(id),
 project_id BIGINT NOT NULL REFERENCES projects(id),
 actor TEXT NOT NULL,
 request_key TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 payload JSONB NOT NULL,
 created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint,
 UNIQUE(project_id,actor,request_key)
);


-- Human decisions authorize one recorded restriction generation. Adding a new
-- hold or revising protected intent invalidates prior authority; cleanup does
-- not renew it and ordinary execution activity cannot renew it either.
CREATE TABLE decision_authorities (
 project_id BIGINT NOT NULL,
 bead_id TEXT NOT NULL,
 generation TEXT NOT NULL,
 authority_epoch BIGINT NOT NULL,
 PRIMARY KEY(project_id,bead_id,generation),
 FOREIGN KEY(project_id,bead_id,generation) REFERENCES needs_resolutions(project_id,bead_id,generation)
);
CREATE FUNCTION commander_human_authority_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE before_labels JSONB; after_labels JSONB; scope_changed BOOLEAN;
BEGIN
 before_labels=commander_human_restrictions(OLD.labels);after_labels=commander_human_restrictions(NEW.labels);
 scope_changed=commander_authority_plan_labels(NEW.labels) IS DISTINCT FROM commander_authority_plan_labels(OLD.labels) OR
 (NEW.title,NEW.description,NEW.acceptance_criteria,NEW.parent_id,NEW.issue_type) IS DISTINCT FROM (OLD.title,OLD.description,OLD.acceptance_criteria,OLD.parent_id,OLD.issue_type) OR
 (OLD.status='closed' AND NEW.status<>'closed') OR (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL) OR
 (jsonb_exists(NEW.labels,'hold') AND NOT jsonb_exists(OLD.labels,'hold'));
 IF scope_changed THEN NEW.human_scope_epoch=OLD.human_scope_epoch+1; END IF;
 IF scope_changed OR NOT after_labels <@ before_labels THEN NEW.human_authority_epoch=OLD.human_authority_epoch+1; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bead_human_authority_epoch BEFORE UPDATE ON bead_tasks FOR EACH ROW EXECUTE FUNCTION commander_human_authority_changed();


CREATE TABLE pitch_authority_bases (
 project_id BIGINT NOT NULL,
 bead_id TEXT NOT NULL,
 version BIGINT NOT NULL,
 scope_epoch BIGINT NOT NULL,
 PRIMARY KEY(project_id,bead_id,version),
 FOREIGN KEY(project_id,bead_id,version) REFERENCES pitches(project_id,bead_id,version)
);
CREATE FUNCTION commander_pitch_authority_basis() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(NEW.project_id);
 INSERT INTO pitch_authority_bases(project_id,bead_id,version,scope_epoch)
 SELECT project_id,id,NEW.version,human_scope_epoch FROM bead_tasks WHERE project_id=NEW.project_id AND id=NEW.bead_id AND deleted_at IS NULL;
 RETURN NULL;
END $$;
CREATE TRIGGER pitch_authority_basis AFTER INSERT ON pitches FOR EACH ROW EXECUTE FUNCTION commander_pitch_authority_basis();

-- Administrative recovery does not reissue approval. Preserve the original
-- journal and record the exact newly raised question with a replay receipt.
CREATE TABLE human_decision_reconciliations (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id BIGINT NOT NULL,
 bead_id TEXT NOT NULL,
 actor TEXT NOT NULL REFERENCES commander_sessions(agent_id),
 request_key TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 source_generation TEXT NOT NULL,
 source_answer_id TEXT NOT NULL,
 reason TEXT NOT NULL,
 next_version BIGINT NOT NULL,
 response JSONB NOT NULL,
 created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint,
 UNIQUE(project_id,bead_id,actor,request_key),
 FOREIGN KEY(project_id,bead_id) REFERENCES bead_tasks(project_id,id),
 FOREIGN KEY(project_id,bead_id,next_version) REFERENCES pitches(project_id,bead_id,version)
);
CREATE TRIGGER human_decision_reconciliations_events AFTER INSERT ON human_decision_reconciliations FOR EACH ROW EXECUTE FUNCTION commander_state_event();

-- New executions bind their runtime once. Imported runs leave these empty
-- until their actual process custody has been verified during migration.
ALTER TABLE agent_runs ADD COLUMN execution_driver TEXT NOT NULL DEFAULT '' CHECK(execution_driver IN('','native'));
ALTER TABLE agent_runs ADD COLUMN execution_runtime TEXT NOT NULL DEFAULT '' CHECK(execution_runtime IN('','claude','codex'));
ALTER TABLE agent_runs ADD COLUMN execution_account TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN execution_model TEXT NOT NULL DEFAULT '';
