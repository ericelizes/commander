#!/usr/bin/env bash
# test-bd-cmdr-namespace.sh — verify bd cmdr= namespace isolation + bd create --parent
#
# Verifies:
#   1. bd set-state <id> cmdr=brainstorming writes label cmdr:brainstorming
#   2. Transition cmdr=planning does NOT remove kind:task (namespace isolation)
#   3. bd list --label cmdr:brainstorming returns bead
#   4. bd create --parent <id> works in bd 1.0.2
#   5. bd link <id1> <id2> --type parent-child (positional, NOT --from/--to)
#
# Run: bash tests/integration/test-bd-cmdr-namespace.sh
# Cost: $0 (local DB)

set -uo pipefail
TEST_DIR=$(mktemp -d)

if ! command -v bd >/dev/null 2>&1; then echo "✗ FAIL: bd not found"; exit 1; fi
echo "bd: $(bd --version 2>/dev/null | head -1)  OS: $(uname -s)"
echo "→ test dir: $TEST_DIR"

cleanup() { rm -rf "$TEST_DIR"; }
trap cleanup EXIT

cd "$TEST_DIR"
git init -q .
git config user.email "test@commander.local"
git config user.name "commander-test"
# --stealth skips the auto-commit that would deadlock in non-interactive mode
# via bd's pre-commit hook → bd export → embedded Dolt recursion.
BD_NON_INTERACTIVE=1 bd init --stealth >/dev/null 2>&1 || \
  BD_NON_INTERACTIVE=1 bd init >/dev/null 2>&1 || true
# Disable auto-export to prevent any export-loop weirdness during tests.
bd config set export.auto false >/dev/null 2>&1 || true

echo ""
echo "── Step 1: create task with kind:task discriminator ──"
TASK_ID=$(bd q "cmdr-ns-test-task" --labels kind:task 2>/dev/null || \
          bd q "cmdr-ns-test-task" -l kind:task 2>/dev/null)
echo "→ task: $TASK_ID"
[ -z "$TASK_ID" ] && { echo "✗ FAIL: bd q failed"; exit 1; }
echo "✓ task created"

echo ""
echo "── Step 2: set cmdr=brainstorming ──"
bd set-state "$TASK_ID" cmdr=brainstorming --reason "test" 2>/dev/null
SHOW=$(bd show "$TASK_ID" 2>/dev/null)
LABEL_WRITTEN="no"; echo "$SHOW" | grep -q "cmdr:brainstorming" && LABEL_WRITTEN="yes"
echo "→ cmdr:brainstorming written: $LABEL_WRITTEN"
[ "$LABEL_WRITTEN" = "no" ] && { echo "✗ FAIL: label not written"; echo "$SHOW" | head -20; exit 1; }

echo ""
echo "── Step 3: bd list --label cmdr:brainstorming ──"
LIST=$(bd list --label cmdr:brainstorming --json 2>/dev/null || \
       bd list -l cmdr:brainstorming 2>/dev/null)
LIST_WORKS="no"; echo "$LIST" | grep -q "$TASK_ID" && LIST_WORKS="yes"
echo "→ list --label works: $LIST_WORKS"

echo ""
echo "── Step 4: transition cmdr=planning — namespace isolation test ──"
bd set-state "$TASK_ID" cmdr=planning --reason "test" 2>/dev/null
SHOW2=$(bd show "$TASK_ID" 2>/dev/null)
PLANNING_SET="no";   echo "$SHOW2" | grep -q "cmdr:planning"   && PLANNING_SET="yes"
KIND_PRESERVED="no"; echo "$SHOW2" | grep -q "kind:task"       && KIND_PRESERVED="yes"
OLD_REMOVED="yes";   echo "$SHOW2" | grep -q "cmdr:brainstorming" && OLD_REMOVED="no"

echo "→ cmdr:planning set:    $PLANNING_SET"
echo "→ kind:task preserved:  $KIND_PRESERVED (CRITICAL — must be yes)"
echo "→ old cmdr label gone:  $OLD_REMOVED"

NAMESPACE_OK="no"
[ "$PLANNING_SET" = "yes" ] && [ "$KIND_PRESERVED" = "yes" ] && NAMESPACE_OK="yes"
if [ "$NAMESPACE_OK" = "no" ]; then
  echo "✗ FAIL: namespace isolation broken — cmdr= transition removed kind:task"
  echo "  Q14 assumption INVALID. Label architecture requires redesign before U2."
  echo "$SHOW2" | head -20
  exit 1
fi
echo "✓ namespaces isolated"

echo ""
echo "── Step 5: bd create --parent (PRIMARY operational bead path) ──"
OP_OUT=$(bd create "operational-test-bead" --parent "$TASK_ID" --labels kind:operational 2>&1 || echo "FAILED")
echo "→ create output: $(echo "$OP_OUT" | head -3)"
CREATE_PARENT="no"
OP_ID=""
if echo "$OP_OUT" | grep -qE "Created issue:"; then
  OP_ID=$(echo "$OP_OUT" | grep -oE 'Created issue: [^ ]+' | sed 's/Created issue: //' | head -1)
  CREATE_PARENT="yes"
  echo "✓ bd create --parent works; child: $OP_ID"
elif echo "$OP_OUT" | grep -qiE "(error|unknown|flag|invalid|FAILED)"; then
  echo "✗ bd create --parent not supported"
  CREATE_PARENT="no"
fi

echo ""
echo "── Step 6: bd link positional args fallback ──"
TASK2=$(bd q "cmdr-ns-test-task-2" 2>/dev/null || echo "")
LINK_WORKS="skipped"
if [ -n "$TASK2" ]; then
  LINK_OUT=$(bd link "$TASK_ID" "$TASK2" --type parent-child 2>&1 || echo "FAILED")
  LINK_WORKS="fail"; echo "$LINK_OUT" | grep -qiE "(error|FAILED|invalid)" || LINK_WORKS="pass"
  echo "→ bd link positional: $LINK_WORKS"
fi

echo ""
echo "════════════════════════════════════════════"
echo "bd cmdr= namespace contract"
echo "  set-state syntax:  bd set-state <id> cmdr=<val>  (= in invocation)"
echo "  label stored:      cmdr:<val>                    (: in storage)"
echo "  label written:     $LABEL_WRITTEN"
echo "  list --label:      $LIST_WORKS"
echo "  namespace isolated:$NAMESPACE_OK  (kind:task preserved=$KIND_PRESERVED)"
echo "  create --parent:   $CREATE_PARENT"
echo "  link positional:   $LINK_WORKS"
echo "════════════════════════════════════════════"
[ "$NAMESPACE_OK" = "yes" ]
