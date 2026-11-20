package harddeny

import "testing"

func TestCheck(t *testing.T) {
	op := Context{Worktree: "/home/exedev/code/vibe-apps/.worktrees.noindex/exec-vm-8"}
	cmdr := Context{}
	cases := []struct {
		name string
		cmd  string
		ctx  Context
		rule string
	}{
		// The command that froze the Commander on 2026-11-17 03:13 UTC runs now: nothing in it is on the list.
		{"nov17 commander loop", `cd /home/exedev/code/vibe-apps; sleep 2; r=$(cmdr execution status --project-id 2 --bead vibe-apps-zq9h.8.35 | python3 -c "import json,sys;print(json.load(sys.stdin)['plan_revision'])"); cmdr execution ready --project-id 2 --bead vibe-apps-zq9h.8.35 --revision $r --jurisdiction-revision 319 --capabilities code 2>&1|tail -1; nohup bash -c 'for i in $(seq 1 30); do sleep 20; done; P=$(mktemp -p . .plan-XXXX.json); cmdr plan apply --file $P; rm $P' > /tmp/close6755.$$.log 2>&1 &
echo started`, cmdr, ""},
		// The Operator commit that prompted on 2026-11-16 19:49 runs too.
		{"nov16 operator commit", `ssh prod-forge-qa-hub-dev-6.exe.xyz 'sudo -u llm-agent bash -c "cd /home/llm-agent/app/dev/worktrees/w; git add -A; git commit -q -m \"Worklists: stored count\"; git log --oneline -1"'; rm -rf /home/exedev/code/vibe-apps/.worktrees.noindex/exec-vm-17/.scratch`, Context{Worktree: "/home/exedev/code/vibe-apps/.worktrees.noindex/exec-vm-17"}, ""},
		{"git status", "git status && go test ./...", op, ""},
		{"push", "git push -u origin feature", op, ""},
		{"force push", "git push --force origin feature", op, "force-push"},
		{"force push -f", "git push -f", op, "force-push"},
		{"force push cluster", "git push -fu origin x", op, "force-push"},
		{"force with lease", "git push --force-with-lease origin x", op, "force-push"},
		{"plus refspec", "git push origin +x:x", op, "force-push"},
		{"push delete", "git push origin --delete old", op, "branch-delete"},
		{"push colon", "git push origin :old", op, "branch-delete"},
		{"branch -D", "git -C /repo branch -D feature", op, "branch-delete"},
		{"branch -d ok", "git branch -d merged", op, ""},
		{"rm in worktree", "rm -rf build node_modules", op, ""},
		{"rm abs in worktree", "rm -rf /home/exedev/code/vibe-apps/.worktrees.noindex/exec-vm-8/dist", op, ""},
		{"rm tmp", "rm -rf /tmp/cc-x", cmdr, ""},
		{"rm home", "rm -rf ~/code", op, "rm-rf"},
		{"rm outside", "rm -rf /home/exedev/code/vibe-apps", op, "rm-rf"},
		{"rm dotdot", "rm -rf ../other", op, "rm-rf"},
		{"rm commander relative", "rm -rf vibe-apps", cmdr, "rm-rf"},
		{"rm not forced", "rm -r build", cmdr, ""},
		{"rm sudo remote", "ssh host 'sudo rm -rf /var/lib/app'", op, "rm-rf"},
		{"drop table", `psql "$DSN" -c "DROP TABLE charts"`, op, "destructive-sql"},
		{"truncate", `psql -h db -d qa_hub -c 'truncate qa_queue_items'`, op, "destructive-sql"},
		{"delete no where", `psql -d app -c "delete from domain_check_runs"`, op, "destructive-sql"},
		{"delete where", `psql -d app -c "delete from domain_check_runs where id = 4"`, op, ""},
		{"drop test db", `psql postgres://postgres@127.0.0.1:55432/commander_trial -c "DROP TABLE x"`, op, ""},
		{"drop in commit msg", `git commit -m "drop table usage from the report"`, op, ""},
		{"systemctl restart", "systemctl --user restart commander.service", cmdr, "service-stop"},
		{"systemctl status", "systemctl --user status commander.service", cmdr, ""},
		{"remote restart", "ssh prod-forge-qa-hub.exe.xyz 'sudo systemctl restart app-forge-app-dev@8102'", op, "service-stop"},
		{"service stop", "sudo service postgresql stop", op, "service-stop"},
		{"reboot", "sudo reboot", op, "vm-stop"},
		{"exe.dev rm", "ssh exe.dev rm prod-forge-qa-hub", cmdr, "vm-stop"},
		{"exe.dev ls", "ssh exe.dev ls --json", cmdr, ""},
		{"pkill cmdrd", "pkill -f cmdrd", cmdr, "service-stop"},
		{"pkill own", "pkill -f 'pytest tests/x'", op, ""},
		{"cmdr restart", "cmdr restart --hard", cmdr, "service-stop"},
		{"cmdr install restart", "bin/staged/cmdr install --yes --restart-cmdrd", cmdr, "service-stop"},
		{"chat stop api", `curl -s -X POST -H "Authorization: Bearer $T" http://localhost:8787/api/chat/commander/stop`, cmdr, "chat-stop"},
		{"chat send api", `curl -s -X POST http://localhost:8787/api/chat/commander/send -d @m.json`, cmdr, ""},
		{"kill pid", "kill -9 12345", op, ""},
		{"kill group", "kill -9 -12345", op, "kill-group"},
		{"kill group dashdash", "kill -TERM -- -12345", op, "kill-group"},
		{"kill zero", "kill 0", op, "kill-group"},
		{"pkill group", "pkill -g 4242", op, "kill-group"},
		{"deploy", "just deploy-prod --confirm-production", op, "prod-deploy"},
		{"deploy py", "UV_FROZEN=1 uv run python scripts/production_workflow.py --confirm-production --source-remote origin", op, "prod-deploy"},
		{"deploy py direct", "python3 scripts/production_workflow.py --confirm-production", op, "prod-deploy"},
		{"deploy plan only", "just deploy-prod --plan-only", Context{}, "prod-deploy"},
		{"deploy approved", "just deploy-prod --confirm-production", Context{Worktree: op.Worktree, DeployAllowed: true}, ""},
		{"deploy over ssh", "ssh dev-6 'cd w && just deploy-prod --confirm-production'", op, "prod-deploy"},
		// Nov 19: an agent fast-forwarded QA Hub master by hand, outside the release process.
		{"push master", "git push origin master", op, "mainline-push"},
		{"push head to master", "git push origin HEAD:master", op, "mainline-push"},
		{"push refs main", "git push origin fix:refs/heads/main", op, "mainline-push"},
		{"push master over ssh", `ssh prod-forge-qa-hub-dev-6.exe.xyz 'sudo -u llm-agent bash -c "cd /home/llm-agent/app/dev/worktrees/w; git push origin HEAD:master"'`, op, "mainline-push"},
		{"push master with -c", "git -c http.extraHeader=x push origin master", op, "mainline-push"},
		{"push feature named mainline-ish", "git push origin fix/master-cleanup", op, ""},
		{"push master in land step", "git push origin HEAD:master", Context{Worktree: op.Worktree, MainlineAllowed: true}, ""},
		{"forge API merge", `curl -u "$auth" -X POST https://forge-git.int.exe.xyz/git/api/v1/repos/forge-admin/qa/pulls/1/merge -d '{"Do":"merge"}'`, op, "mainline-push"},
		{"forge API merge in a script run elsewhere", "cat > /tmp/m.sh <<'EOF'\nB=https://forge-git.int.exe.xyz/git/api/v1/repos/forge-admin/qa\ncurl -X POST \"$B/pulls/1/merge\" -d '{}'\nEOF\nscp -q /tmp/m.sh dev-2:/tmp/m.sh; ssh dev-2 'bash /tmp/m.sh'", op, "mainline-push"},
		{"tea merge", "tea pr merge 1", op, "mainline-push"},
		{"forge API merge in land step", `curl -X POST https://forge-git.int.exe.xyz/git/api/v1/repos/forge-admin/qa/pulls/1/merge`, Context{Worktree: op.Worktree, MainlineAllowed: true}, ""},
		{"forge API PR read", `curl -u "$auth" https://forge-git.int.exe.xyz/git/api/v1/repos/forge-admin/qa/pulls/1`, op, ""},
		{"GitHub PR merge", "gh pr merge 3954 --squash", op, ""},
	}
	for _, c := range cases {
		v := Check(c.cmd, c.ctx)
		if v.Rule != c.rule {
			t.Errorf("%s: Check(%q) = %q (%s), want %q", c.name, c.cmd, v.Rule, v.Why, c.rule)
		}
		if v.Denied() && v.Message() == "" {
			t.Errorf("%s: a refusal must say why", c.name)
		}
	}
}

func TestDevVMRestartAllowed(t *testing.T) {
	for _, c := range []struct {
		cmd    string
		denied bool
	}{
		{"ssh exe.dev restart prod-forge-orders-management-hub-dev2", false},
		{"ssh exe.dev restart prod-forge-qa-hub-dev", false},
		{"ssh exe.dev restart prod-forge-orders-management-hub", true},
		{"ssh exe.dev stop prod-forge-orders-management-hub-dev2", true},
		{"ssh exe.dev rm prod-forge-orders-management-hub-dev2", true},
		{"ssh exe.dev restart prod-forge-x-dev2; ssh exe.dev rm prod-forge-x", true},
	} {
		v := Check(c.cmd, Context{})
		if v.Denied() != c.denied {
			t.Errorf("%q: denied=%v, want %v (%s)", c.cmd, v.Denied(), c.denied, v.Rule)
		}
	}
}
