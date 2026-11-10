package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// The compatibility entry point intentionally supports reads and attributed
// comments only. Task mutations retain their existing guarded cmdr commands.
func runBeadsCompat(args []string, stdout, stderr *os.File) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir, e := os.Getwd()
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	pos := []string{}
	opts := beads.ListOptions{Status: "open"}
	readonly := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(pos) == 3 && pos[0] == "comments" && pos[1] == "add" {
			pos = append(pos, a)
			continue
		}
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", errors.New("missing flag value")
			}
			i++
			return args[i], nil
		}
		switch a {
		case "--project-dir", "-C", "--directory":
			dir, e = next()
		case "--db":
			var db string
			db, e = next()
			if e == nil {
				dir = filepath.Dir(filepath.Dir(db))
			}
		case "--json", "--no-color", "--quiet", "-q":
		case "--readonly":
			readonly = true
		case "--all":
			opts.Status = "all"
		case "--status":
			opts.Status, e = next()
		case "--label":
			var l string
			l, e = next()
			opts.Labels = append(opts.Labels, l)
		case "--label-any":
			var l string
			l, e = next()
			opts.LabelAny = append(opts.LabelAny, l)
		case "--type":
			opts.Type, e = next()
		case "--limit":
			var v string
			v, e = next()
			if e == nil {
				opts.Limit, e = strconv.Atoi(v)
			}
		default:
			if strings.HasPrefix(a, "--") {
				e = fmt.Errorf("unsupported bead read option %s", a)
			} else {
				pos = append(pos, a)
			}
		}
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
	}
	if len(pos) == 0 {
		fmt.Fprintln(stderr, "usage: cmdr bead --project-dir PATH show|list|comments ...")
		return 1
	}
	client := beads.New()
	var result any
	switch pos[0] {
	case "show":
		if len(pos) != 2 {
			e = errors.New("show requires one bead")
			break
		}
		var b *beads.Bead
		b, e = client.Show(ctx, dir, pos[1])
		if e != nil {
			break
		}
		var details map[string]beads.Detail
		details, e = client.Details(ctx, dir, []string{pos[1]})
		if e != nil {
			break
		}
		result = struct {
			*beads.Bead
			Comments []beads.Comment `json:"comments"`
		}{b, details[pos[1]].Comments}
		b.Dependencies = details[pos[1]].Deps
	case "list":
		result, e = client.List(ctx, dir, opts)
	case "comments":
		if len(pos) >= 2 && pos[1] == "add" {
			if readonly {
				e = errors.New("read-only request cannot add comments")
				break
			}
			if len(pos) != 4 {
				e = errors.New("comments add requires a bead and one text argument")
				break
			}
			e = client.AddComment(ctx, dir, pos[2], pos[3])
			result = map[string]bool{"recorded": e == nil}
		} else {
			if len(pos) == 3 && pos[1] == "list" {
				pos = pos[1:]
			}
			if len(pos) != 2 {
				e = errors.New("comments requires one bead")
				break
			}
			result, e = client.Comments(ctx, dir, pos[1])
		}
	default:
		e = errors.New("task mutations must use guarded cmdr internal-create-objective, internal-set-objective, internal-close-objective, internal-dep or internal-gate commands")
	}
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = json.NewEncoder(stdout).Encode(result); e != nil {
		return 1
	}
	return 0
}
