-- PROVISIONAL migration 3: unpublished coordinated candidate. The independent
-- places candidate calls itself v2; this candidate rejects v2. Unify/reassign before deployment;
-- this number DOES NOT imply compatibility with that candidate.
ALTER TABLE bead_dependencies ADD COLUMN target_project_id BIGINT;
UPDATE bead_dependencies SET target_project_id=project_id;
ALTER TABLE bead_dependencies DROP CONSTRAINT bead_dependencies_project_id_depends_on_id_fkey;
ALTER TABLE bead_dependencies ADD CONSTRAINT bead_dependencies_target_fkey
 FOREIGN KEY(target_project_id,depends_on_id) REFERENCES bead_tasks(project_id,id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE bead_dependencies ADD CONSTRAINT bead_dependencies_local_parent CHECK(type <> 'parent-child' OR project_id=target_project_id);
CREATE INDEX bead_dependencies_qualified_target ON bead_dependencies(target_project_id,depends_on_id);
CREATE TABLE qualified_graph_revision(id BIGINT PRIMARY KEY CHECK(id=1),revision BIGINT NOT NULL DEFAULT 0);
INSERT INTO qualified_graph_revision(id) VALUES(1);
CREATE FUNCTION commander_qualified_lock() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(734031823);
 IF TG_TABLE_NAME='bead_dependencies' AND TG_OP <> 'DELETE' THEN
  NEW.target_project_id := COALESCE(NEW.target_project_id,NEW.project_id);
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END $$;
CREATE FUNCTION commander_qualified_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE qualified_graph_revision SET revision=revision+1 WHERE id=1;
 RETURN NULL;
END $$;
CREATE TRIGGER qualified_tasks_lock BEFORE INSERT OR UPDATE OR DELETE ON bead_tasks FOR EACH STATEMENT EXECUTE FUNCTION commander_qualified_lock();
CREATE TRIGGER qualified_projects_lock BEFORE INSERT OR UPDATE OR DELETE ON projects FOR EACH STATEMENT EXECUTE FUNCTION commander_qualified_lock();
CREATE TRIGGER qualified_dependencies_lock BEFORE INSERT OR UPDATE OR DELETE ON bead_dependencies FOR EACH ROW EXECUTE FUNCTION commander_qualified_lock();
CREATE TRIGGER qualified_managers_lock BEFORE INSERT OR UPDATE OR DELETE ON workstream_managers FOR EACH STATEMENT EXECUTE FUNCTION commander_qualified_lock();
CREATE TRIGGER qualified_tasks_revision AFTER INSERT OR UPDATE OR DELETE ON bead_tasks FOR EACH STATEMENT EXECUTE FUNCTION commander_qualified_revision();
CREATE TRIGGER qualified_projects_revision AFTER INSERT OR UPDATE OR DELETE ON projects FOR EACH STATEMENT EXECUTE FUNCTION commander_qualified_revision();
CREATE TRIGGER qualified_dependencies_revision AFTER INSERT OR UPDATE OR DELETE ON bead_dependencies FOR EACH STATEMENT EXECUTE FUNCTION commander_qualified_revision();
ALTER TABLE bead_dependencies ALTER COLUMN target_project_id SET NOT NULL;
