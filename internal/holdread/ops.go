package holdread

import (
	"slices"
	"strings"

	"github.com/example-user/commander/internal/holding"
)

// EditsFromOps simulates a door write (bd/br argv, binary left out) on the predicate's beads:
// update (--status, --add-label, --remove-label, --set-labels, --parent), close, reopen, delete,
// defer/undefer and dep add|remove. Anything else changes nothing it can see. ok is false when an
// op cannot be simulated (the caller then judges nothing rather than guess).
func EditsFromOps(ops [][]string) (map[string]holding.Edit, bool) {
	var steps []struct {
		id string
		e  holding.Edit
	}
	add := func(id string, e holding.Edit) {
		steps = append(steps, struct {
			id string
			e  holding.Edit
		}{id, e})
	}
	for _, args := range ops {
		args = withoutGlobals(args)
		if len(args) < 2 {
			return nil, false
		}
		switch verb, id := args[0], args[1]; verb {
		case "update":
			e, ok := updateEdit(args[2:])
			if !ok {
				return nil, false
			}
			add(id, e)
		case "close", "delete":
			for _, x := range positional(args[1:]) {
				add(x, func(b *holding.Bead) { b.Status = holding.StatusClosed })
			}
		case "reopen", "undefer":
			add(id, func(b *holding.Bead) { b.Status = "open" })
		case "defer":
			add(id, func(b *holding.Bead) { b.Status = holding.StatusDeferred })
		case "dep":
			p := positional(args[1:])
			if len(p) < 3 {
				return nil, false
			}
			sub, a, on := p[0], p[1], p[2]
			switch sub {
			case "add":
				if flagVal(args, "--type") == "parent-child" {
					add(a, func(b *holding.Bead) { b.Parent = on })
					continue
				}
				add(a, func(b *holding.Bead) {
					if !slices.Contains(b.BlockedBy, on) {
						b.BlockedBy = append(b.BlockedBy, on)
					}
				})
			case "remove", "rm", "delete":
				add(a, func(b *holding.Bead) {
					b.BlockedBy = slices.DeleteFunc(slices.Clone(b.BlockedBy), func(x string) bool { return x == on })
				})
			default:
				return nil, false
			}
		case "comments", "comment", "label":
			if verb == "label" {
				p := positional(args[1:])
				if len(p) < 3 {
					return nil, false
				}
				on := p[0] == "add"
				id, l := p[1], p[2]
				add(id, labelEdit([]string{l}, on))
			}
		default:
			return nil, false
		}
	}
	out := map[string]holding.Edit{}
	for _, st := range steps {
		prev, e := out[st.id], st.e
		out[st.id] = func(b *holding.Bead) {
			if prev != nil {
				prev(b)
			}
			e(b)
		}
	}
	return out, true
}

func updateEdit(args []string) (holding.Edit, bool) {
	var es []holding.Edit
	for i := 0; i < len(args); i++ {
		flag, val, inline := strings.Cut(args[i], "=")
		if !inline {
			if !strings.HasPrefix(flag, "--") || i+1 >= len(args) {
				continue
			}
			val = args[i+1]
			i++
		}
		switch flag {
		case "--status", "-s":
			v := val
			es = append(es, func(b *holding.Bead) { b.Status = v })
		case "--add-label":
			es = append(es, labelEdit(strings.Split(val, ","), true))
		case "--remove-label":
			es = append(es, labelEdit(strings.Split(val, ","), false))
		case "--set-labels":
			ls := strings.Split(val, ",")
			es = append(es, func(b *holding.Bead) { b.Labels = slices.Clone(ls); Derive(b) })
		case "--parent":
			v := val
			es = append(es, func(b *holding.Bead) { b.Parent = v })
		case "--assignee", "-a":
			// read nowhere by the predicate
		}
	}
	return func(b *holding.Bead) {
		for _, e := range es {
			e(b)
		}
		Derive(b)
	}, true
}

func labelEdit(ls []string, on bool) holding.Edit {
	return func(b *holding.Bead) {
		for _, l := range ls {
			has := slices.Contains(b.Labels, l)
			switch {
			case on && !has:
				b.Labels = append(slices.Clone(b.Labels), l)
			case !on && has:
				b.Labels = slices.DeleteFunc(slices.Clone(b.Labels), func(x string) bool { return x == l })
			}
		}
		Derive(b)
	}
}

// withoutGlobals drops leading global flags (--db x, -C x, --json, --quiet ...).
func withoutGlobals(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if strings.Contains(args[0], "=") || args[0] == "--json" || args[0] == "-q" || args[0] == "--quiet" {
			args = args[1:]
			continue
		}
		if len(args) < 2 {
			return nil
		}
		args = args[2:]
	}
	return args
}

func positional(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !boolFlag(args[i]) {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func boolFlag(f string) bool {
	switch f {
	case "--force", "--json", "--quiet", "-q", "--silent":
		return true
	}
	return false
}

func flagVal(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v
		}
	}
	return ""
}

// addsHolderOnly reports whether an op only adds a holder (a wait, an owner, a card, a blocker),
// so holder-first ordering may run it before the ops that drop one (I3, make before break).
func addsHolderOnly(args []string) bool {
	args = withoutGlobals(args)
	if len(args) < 2 {
		return false
	}
	switch args[0] {
	case "dep":
		p := positional(args[1:])
		return len(p) >= 3 && p[0] == "add" && flagVal(args, "--type") != "parent-child"
	case "update":
		any := false
		for i := 2; i < len(args); i++ {
			flag, val, inline := strings.Cut(args[i], "=")
			if !inline && i+1 < len(args) {
				val = args[i+1]
				i++
			}
			if flag != "--add-label" {
				return false
			}
			for _, l := range strings.Split(val, ",") {
				if !strings.HasPrefix(l, "waiting:") && !strings.HasPrefix(l, "w:") && !strings.HasPrefix(l, "owner:") && l != "needs-user" {
					return false
				}
			}
			any = true
		}
		return any
	}
	return false
}

// HolderFirst orders ops so those that only add a holder run first, the rest in their order.
func HolderFirst(ops [][]string) [][]string {
	var first, rest [][]string
	for _, op := range ops {
		if addsHolderOnly(op) {
			first = append(first, op)
		} else {
			rest = append(rest, op)
		}
	}
	return append(first, rest...)
}
