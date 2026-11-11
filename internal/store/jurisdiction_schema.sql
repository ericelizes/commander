-- Planning ownership is independent of execution claims and runtime presence.
-- A project revision serializes jurisdiction transfers, without locking the
-- projects row before bead locks (bead event triggers lock that row last).
CREATE TABLE commander_sessions (
 agent_id TEXT PRIMARY KEY,
 title TEXT NOT NULL,
 created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint
);
INSERT INTO commander_sessions(agent_id,title) VALUES('commander','Commander');
CREATE TABLE jurisdiction_projects (
 project_id BIGINT PRIMARY KEY REFERENCES projects(id),
 revision BIGINT NOT NULL DEFAULT 1,
 default_commander TEXT NOT NULL DEFAULT 'commander' REFERENCES commander_sessions(agent_id)
);
CREATE TABLE commander_jurisdictions (
 project_id BIGINT NOT NULL,
 bead_id TEXT NOT NULL,
 commander_id TEXT NOT NULL REFERENCES commander_sessions(agent_id),
 origin TEXT NOT NULL DEFAULT 'explicit' CHECK(origin IN('explicit','legacy')),
 PRIMARY KEY(project_id,bead_id),
 FOREIGN KEY(project_id,bead_id) REFERENCES bead_tasks(project_id,id)
);
CREATE INDEX commander_jurisdictions_owner ON commander_jurisdictions(commander_id,project_id);
CREATE TABLE jurisdiction_history (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id BIGINT NOT NULL REFERENCES projects(id),
 bead_id TEXT NOT NULL,
 revision BIGINT NOT NULL,
 actor TEXT NOT NULL,
 previous_commander TEXT NOT NULL,
 next_commander TEXT NOT NULL,
 inherit BOOLEAN NOT NULL,
 reason TEXT NOT NULL,
 occurred_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint,
 UNIQUE(project_id,revision)
);

-- Real parent edges define scope. Explicit nested overrides remain intact
-- when a parent scope is handed over. IDs never imply ancestry.
CREATE FUNCTION commander_planning_owner(p BIGINT,b TEXT) RETURNS TEXT LANGUAGE sql STABLE AS $$
 WITH RECURSIVE ancestry(id,parent_id,depth,path) AS (
  SELECT id,parent_id,0,ARRAY[id] FROM bead_tasks WHERE project_id=p AND id=b AND deleted_at IS NULL
  UNION ALL SELECT t.id,t.parent_id,a.depth+1,a.path||t.id FROM bead_tasks t JOIN ancestry a ON t.id=a.parent_id
  WHERE t.project_id=p AND t.deleted_at IS NULL AND NOT t.id=ANY(a.path)
 ) SELECT COALESCE(
  (SELECT j.commander_id FROM ancestry a JOIN commander_jurisdictions j ON j.project_id=p AND j.bead_id=a.id ORDER BY a.depth LIMIT 1),
  (SELECT default_commander FROM jurisdiction_projects WHERE project_id=p),'commander')
$$;
CREATE TRIGGER commander_sessions_events AFTER INSERT OR UPDATE OR DELETE ON commander_sessions FOR EACH ROW EXECUTE FUNCTION commander_state_event();
CREATE TRIGGER jurisdiction_events AFTER INSERT OR UPDATE OR DELETE ON commander_jurisdictions FOR EACH ROW EXECUTE FUNCTION commander_state_event();
CREATE TRIGGER jurisdiction_history_events AFTER INSERT ON jurisdiction_history FOR EACH ROW EXECUTE FUNCTION commander_state_event();

-- Preserve current scope boundaries on upgrade. Legacy role names remain
-- runtime addresses only, until their conversations are handed over explicitly.
INSERT INTO commander_sessions(agent_id,title)
 SELECT DISTINCT am_role,am_role FROM workstream_managers WHERE am_role<>'' ON CONFLICT DO NOTHING;
INSERT INTO jurisdiction_projects(project_id) SELECT id FROM projects;
INSERT INTO commander_jurisdictions(project_id,bead_id,commander_id,origin)
 SELECT m.project_id,m.workstream_bead_id,m.am_role,'legacy' FROM workstream_managers m
 JOIN bead_tasks b ON b.project_id=m.project_id AND b.id=m.workstream_bead_id WHERE m.am_role<>'';

-- Consolidation receipts retain the exact reviewed migration, even after the
-- installation activates or later scope transfers change current ownership.
CREATE TABLE jurisdiction_consolidations (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 project_id BIGINT NOT NULL REFERENCES projects(id),
 actor TEXT NOT NULL,
 request_key TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 request JSONB NOT NULL,
 response JSONB NOT NULL,
 created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint,
 UNIQUE(project_id,actor,request_key)
);
CREATE TRIGGER jurisdiction_consolidations_events AFTER INSERT ON jurisdiction_consolidations FOR EACH ROW EXECUTE FUNCTION commander_state_event();
