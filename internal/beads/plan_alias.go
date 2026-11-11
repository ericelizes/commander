package beads

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var planAliasName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type planValidatorKey struct{}
type PlanValidator func(context.Context, [][]string) error

func WithPlanValidator(ctx context.Context, v PlanValidator) context.Context {
	return context.WithValue(ctx, planValidatorKey{}, v)
}

// References occur only in identity positions. A title or brief containing
// "$goal" remains literal text.
func resolvePlanOperation(op []string, aliases map[string]string) ([]string, string, error) {
	if len(op) < 2 {
		return nil, "", errors.New("incomplete planning operation")
	}
	replace := func(value string) (string, error) {
		if !strings.HasPrefix(value, "$") {
			return value, nil
		}
		v, ok := aliases[strings.TrimPrefix(value, "$")]
		if !ok {
			return "", fmt.Errorf("unknown or forward planning reference %s", value)
		}
		return v, nil
	}
	if op[0] == "comments" {
		if len(op) != 4 || op[1] != "add" {
			return nil, "", errors.New("invalid planning comment")
		}
		out := append([]string(nil), op...)
		value, e := replace(out[2])
		if e != nil {
			return nil, "", e
		}
		out[2] = value
		return out, "", nil
	}
	out := []string{op[0]}
	positions := []int{}
	ref := ""
	for i := 1; i < len(op); i++ {
		arg := op[i]
		if !strings.HasPrefix(arg, "--") {
			positions = append(positions, len(out))
			out = append(out, arg)
			continue
		}
		key, value, equal := strings.Cut(arg, "=")
		boolean := key == "--force" || key == "--silent" || key == "--no-inherit-labels" || key == "--claim"
		if !equal && !boolean {
			if i+1 >= len(op) {
				return nil, "", fmt.Errorf("missing value for %s", key)
			}
			i++
			value = op[i]
		}
		if key == "--ref" {
			if op[0] != "create" || ref != "" || !planAliasName.MatchString(value) {
				return nil, "", errors.New("invalid or duplicate creation alias")
			}
			ref = value
			continue
		}
		if key == "--parent" {
			var e error
			value, e = replace(value)
			if e != nil {
				return nil, "", e
			}
		}
		if equal {
			out = append(out, key+"="+value)
		} else {
			out = append(out, key)
			if !boolean {
				out = append(out, value)
			}
		}
	}
	target := []int{}
	switch op[0] {
	case "create":
	case "dep":
		target = []int{1, 2}
	case "link":
		target = []int{0, 1}
	default:
		target = []int{0}
	}
	for _, index := range target {
		if index >= len(positions) {
			return nil, "", errors.New("missing planning identity")
		}
		i := positions[index]
		v, e := replace(out[i])
		if e != nil {
			return nil, "", e
		}
		out[i] = v
	}
	if ref != "" {
		out = append(out, "--silent")
	}
	return out, ref, nil
}
