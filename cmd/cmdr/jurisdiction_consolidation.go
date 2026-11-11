package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/store"
)

func runJurisdictionConsolidation(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("jurisdiction migration", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pid := fs.Int64("project-id", 0, "")
	file := fs.String("file", "-", "")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	var body []byte
	method, path := http.MethodGet, api.LegacyPlanningInventoryPath+"?project_id="+strconv.FormatInt(*pid, 10)
	if args[0] == "inventory" {
		if *pid <= 0 {
			return 2
		}
	} else {
		var input io.Reader = os.Stdin
		if *file != "-" {
			f, e := os.Open(*file)
			if e != nil {
				fmt.Fprintln(stderr, e)
				return 1
			}
			defer f.Close()
			input = f
		}
		var req store.JurisdictionConsolidation
		d := json.NewDecoder(io.LimitReader(input, 128<<10))
		d.DisallowUnknownFields()
		if e := d.Decode(&req); e != nil {
			fmt.Fprintln(stderr, e)
			return 2
		}
		if e := d.Decode(new(any)); e != io.EOF {
			return 2
		}
		env := []string{}
		for _, key := range []string{"COMMANDER_ROLE", "COMMANDER_RUN_ID", "COMMANDER_RUN_SESSION", "COMMANDER_MANAGER_ROLE"} {
			env = append(env, key+"="+os.Getenv(key))
		}
		body, _ = json.Marshal(struct {
			store.JurisdictionConsolidation
			Env []string `json:"env"`
		}{req, env})
		method, path = http.MethodPost, api.JurisdictionConsolidationPath
	}
	home := agentrt.CommanderHome()
	base, e := os.ReadFile(filepath.Join(home, "api-url"))
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	token, e := os.ReadFile(filepath.Join(home, "api-token"))
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	request, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(strings.TrimSpace(string(base)), "/")+path, bytes.NewReader(body))
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, e := http.DefaultClient.Do(request)
	if e != nil {
		fmt.Fprintln(stderr, "canonical planning migration unavailable; no local fallback was run:", e)
		return 1
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if response.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, string(raw))
		return 1
	}
	if _, e = stdout.Write(raw); e != nil {
		return 1
	}
	return 0
}
