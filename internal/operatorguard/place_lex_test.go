package operatorguard

import (
	"math/rand"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLexPlaceMatchesTheShell: any line the lexer accepts as plain words
// reaches the shell's argv as exactly those words (a ~ the guard expands as
// the shell does). Random lines over the characters that matter.
func TestLexPlaceMatchesTheShell(t *testing.T) {
	for _, sh := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("%s not installed: skipped", sh)
			continue
		}
		home := t.TempDir()
		run := PlaceRun{Dir: t.TempDir(), Home: home}
		rng := rand.New(rand.NewSource(1))
		alphabet := []string{"a", "b", ".", "/", "~", "=", ":", ",", "^", "%", "@", "+", "-", "1", "'", "\"", " ", "x y", "{", "}", "*", "$", "\\", "!", "#", "[", "(", "..", "~/", "'~'", "\"a b\""}
		checked := 0
		for n := 0; n < 4000 && checked < 600; n++ {
			var b strings.Builder
			for k := 1 + rng.Intn(8); k > 0; k-- {
				b.WriteString(alphabet[rng.Intn(len(alphabet))])
			}
			line := b.String()
			segs, why := lexPlace(line)
			if why != "" || len(segs) != 1 || len(segs[0].Out)+len(segs[0].In) > 0 {
				continue
			}
			checked++
			cmd := exec.Command(path, "-c", "printf '%s\\0' "+line)
			cmd.Dir = run.Dir
			cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
			out, err := cmd.Output()
			if err != nil {
				t.Errorf("%s rejected %q the lexer accepted: %v", sh, line, err)
				continue
			}
			got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			if len(out) == 0 {
				got = nil
			}
			want := make([]string, 0, len(segs[0].Words))
			for i, w := range segs[0].Words {
				if strings.HasPrefix(w, "~") {
					// the same path; the shell keeps a trailing / or ..
					want = append(want, run.tilde(w))
					if i < len(got) {
						got[i] = filepath.Clean(got[i])
					}
					continue
				}
				want = append(want, w)
			}
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("%s: %q gives %q, the guard read %q", sh, line, got, want)
			}
		}
		if checked < 100 {
			t.Fatalf("%s: only %d lines checked", sh, checked)
		}
	}
}
