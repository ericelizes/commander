package daemon

import (
	"context"
	"log"
	"strings"

	"github.com/example-user/commander/internal/store"
)

// withdrawQuestionCards takes a card off the user's list once the agent's
// question behind it was answered or superseded: no card asks for something
// no one waits on.
func withdrawQuestionCards(ctx context.Context, list func(context.Context) ([]store.OpenQuestion, error), run func(ctx context.Context, args ...string) error) int {
	cards, err := list(ctx)
	if err != nil || len(cards) == 0 {
		return 0
	}
	n := 0
	for _, c := range cards {
		if err := run(ctx, "internal-withdraw-card", "--project", c.Project, "--bead", c.Bead); err != nil {
			log.Printf("question-card: %s %s: %v", c.Project, c.Bead, err)
			continue
		}
		log.Printf("question-card: %s %s: its question was answered or superseded, so the card is withdrawn", c.Project, c.Bead)
		n++
	}
	return n
}

// answerText is what the user's card answer tells the agent.
func answerText(choice, text string) string {
	choice, text = strings.TrimSpace(choice), strings.TrimSpace(text)
	switch {
	case choice != "" && text != "":
		return choice + ": " + text
	case text != "":
		return text
	}
	return choice
}
