package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/store"
)

const autoAcceptEvery = 15 * time.Second

// acceptJob closes one finished agent step.
type acceptJob = store.AcceptCandidate

// acceptReason is the close reason of an accepted agent step: its own report.
func acceptReason(c acceptJob) string {
	line := c.Line
	if len(line) > 300 {
		line = line[:300]
	}
	return "Accepted automatically: " + line
}

// acceptResults closes every agent step whose run ended with a clean final
// report, through the guarded close command (the same validation and
// workflow checks as any close), and reports how many it closed. The report
// stays on the step as its result.
func acceptResults(ctx context.Context, list func(context.Context) ([]acceptJob, error), closeStep func(ctx context.Context, project, bead, reason string) error) int {
	jobs, err := list(ctx)
	if err != nil {
		log.Printf("auto-accept: list: %v", err)
		return 0
	}
	n := 0
	for _, j := range jobs {
		if err := closeStep(ctx, j.Project, j.Bead, acceptReason(j)); err != nil {
			log.Printf("auto-accept: %s: %v", j.Bead, err)
			continue
		}
		log.Printf("auto-accept: %s %q closed on its agent's final report", j.Bead, j.Title)
		n++
	}
	return n
}

// cmdrCommand runs the installed cmdr beside cmdrd.
func cmdrCommand(ctx context.Context, args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(c, filepath.Join(filepath.Dir(self), "cmdr"), args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, firstLine(string(out), 400))
	}
	return nil
}

// closeWithReason closes a step through cmdr internal-close-objective.
func closeWithReason(ctx context.Context, project, bead, reason string) error {
	return cmdrCommand(ctx, "internal-close-objective", "--project", project, "--bead", bead, "--reason", reason)
}

// runAutoAccept accepts finished agent steps every autoAcceptEvery, and starts
// the steps they unblock at once.
func (d *Daemon) runAutoAccept(ctx context.Context) {
	t := time.NewTicker(autoAcceptEvery)
	defer t.Stop()
	for {
		if d.attempted == nil {
			d.attempted = &answerTries{}
		}
		withdrawQuestionCards(ctx, d.store.StaleQuestionCards, d.tryOnce)
		applyAnswers(ctx, d.store.PendingAnswers, d.attempted, d.answerQuestion)
		closed := closeAnsweredGates(ctx, d.store.AnsweredGates, d.tryClose, d.finishRejectedGate)
		closed += reviseSentBackReviews(ctx, d.store.ReviewsSentBack, applySystemPlan, d.store.RerunAfterRevision)
		closed += rerunUnblocked(ctx, d.store.BlockedResultsUnblocked, d.store.RerunAfterRevision)
		if acceptResults(ctx, d.store.AgentResultsToAccept, d.tryClose)+closed > 0 {
			d.store.NotifyStateChanged(ctx)
			d.autoDispatchOnce(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// answerTries remembers answers cmdrd already tried to apply, so a failing
// one is retried at most every ten minutes.
type answerTries struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// applyAnswers finishes every recorded answer of the user's that its step has
// not taken yet, through the same command the Commander used to run:
// applying the user's own answer needs no one's judgment.
func applyAnswers(ctx context.Context, list func(context.Context) ([]store.PendingAnswer, error), tries *answerTries, answerQuestion func(context.Context, store.PendingAnswer)) {
	answers, err := list(ctx)
	if err != nil || len(answers) == 0 {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmdr := filepath.Join(filepath.Dir(self), "cmdr")
	for _, a := range answers {
		key := a.Project + ":" + a.Bead
		tries.mu.Lock()
		if tries.last == nil {
			tries.last = map[string]time.Time{}
		}
		recent := time.Since(tries.last[key]) < 10*time.Minute
		if !recent {
			tries.last[key] = time.Now()
		}
		tries.mu.Unlock()
		if recent {
			continue
		}
		if answerQuestion != nil {
			answerQuestion(ctx, a)
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		cmd := exec.CommandContext(c, cmdr, "internal-resolve-needs", "--project", a.Project, "--bead", a.Bead, "--action", a.Action, "--resume-only")
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			log.Printf("apply-answer: %s %s: %v: %s", a.Project, a.Bead, err, firstLine(string(out), 300))
			continue
		}
		log.Printf("apply-answer: %s %s applied the user's recorded answer", a.Project, a.Bead)
	}
}

// tryOnce runs a cmdr command at most every ten minutes per command line, so
// a refused one is not retried every pass.
func (d *Daemon) tryOnce(ctx context.Context, args ...string) error {
	key := strings.Join(args, " ")
	d.attempted.mu.Lock()
	if d.attempted.last == nil {
		d.attempted.last = map[string]time.Time{}
	}
	recent := time.Since(d.attempted.last[key]) < 10*time.Minute
	if !recent {
		d.attempted.last[key] = time.Now()
	}
	d.attempted.mu.Unlock()
	if recent {
		return fmt.Errorf("retried at most every ten minutes")
	}
	return cmdrCommand(ctx, args...)
}

// tryClose closes a step at most every ten minutes per step, so a step whose
// close is refused is not retried every pass.
func (d *Daemon) tryClose(ctx context.Context, project, bead, reason string) error {
	if d.attempted == nil {
		d.attempted = &answerTries{}
	}
	key := "close:" + project + ":" + bead
	d.attempted.mu.Lock()
	if d.attempted.last == nil {
		d.attempted.last = map[string]time.Time{}
	}
	recent := time.Since(d.attempted.last[key]) < 10*time.Minute
	if !recent {
		d.attempted.last[key] = time.Now()
	}
	d.attempted.mu.Unlock()
	if recent {
		return fmt.Errorf("retried at most every ten minutes")
	}
	return closeWithReason(ctx, project, bead, reason)
}

// answerQuestion hands the user's answer to the agent whose blocking question
// the card carried, as the step's planning owner, which resumes the agent.
func (d *Daemon) answerQuestion(ctx context.Context, a store.PendingAnswer) {
	id, owner, err := d.store.PendingQuestionOn(ctx, a.Project, a.Bead)
	if err != nil || id == "" || owner == "" {
		return
	}
	text := answerText(a.Choice, a.Text)
	if text == "" {
		return
	}
	if _, err := d.store.AnswerQuestion(ctx, id, owner, "The user answered: "+text, true); err != nil {
		log.Printf("question-answer: %s %s: %v", a.Project, a.Bead, err)
		return
	}
	log.Printf("question-answer: %s %s: the user's answer went to the agent", a.Project, a.Bead)
}
