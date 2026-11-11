-- Version 3: upgrade the already-shipped jurisdiction schema in place.
-- Binding classifier evidence to an immutable run cannot rewrite old chat data.
ALTER TABLE agent_sessions ADD COLUMN run_id BIGINT NOT NULL DEFAULT 0 CHECK(run_id>=0);
CREATE INDEX agent_runs_current_actor ON agent_runs((replace(agent_name,'.','-'))) WHERE status='running';

CREATE TABLE planning_runtime_retirements (
 agent_id TEXT PRIMARY KEY REFERENCES commander_sessions(agent_id),
 session_id TEXT NOT NULL,
 retired_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM clock_timestamp())::bigint
);
CREATE TRIGGER planning_runtime_retirements_events AFTER INSERT ON planning_runtime_retirements FOR EACH ROW EXECUTE FUNCTION commander_state_event();

DROP TRIGGER agent_accounts_events ON agent_accounts;
CREATE TRIGGER agent_accounts_events AFTER INSERT OR UPDATE OR DELETE ON agent_accounts FOR EACH ROW EXECUTE FUNCTION commander_state_event();

-- Historical closure is not fresh recovery work. Retain saved answer obligations
-- and every bead's progress/assignment/result/authority facts unchanged.
UPDATE bead_supervision s SET state='resolved',due_at=9223372036854775807,
 lease_until=0,lease_token=''
 FROM bead_tasks b WHERE s.project_id=b.project_id AND s.bead_id=b.id
 AND ((b.status='closed' AND NOT commander_pending_human_answer(b.project_id,b.id)) OR b.deleted_at IS NOT NULL)
 AND (s.state<>'resolved' OR s.due_at<>9223372036854775807 OR s.lease_until<>0 OR s.lease_token<>'');

-- Uninitialized projects inherit the same default as commander_planning_owner.
-- Retirement and runtime context must agree about that implicit jurisdiction.
CREATE FUNCTION commander_identity_owns(role TEXT) RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM jurisdiction_projects WHERE default_commander=role)
 OR EXISTS(SELECT 1 FROM commander_jurisdictions WHERE commander_id=role)
 OR (role='commander' AND EXISTS(SELECT 1 FROM projects p WHERE NOT EXISTS(SELECT 1 FROM jurisdiction_projects j WHERE j.project_id=p.id)))
$$;
