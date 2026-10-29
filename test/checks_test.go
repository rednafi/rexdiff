package test

import (
	"regexp"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
)

// TestPRManyChecks checks that the PR tab counts every check, not only the
// 100 that it keeps, and names the failures past the first 100 checks:
// gh reports 100 passing checks and then 150 failing ones.
func TestPRManyChecks(t *testing.T) {
	repo := newRepo(t)
	testgit.Git(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	bin := tools(t, map[string]string{
		"gh": `printf '{"number": 7, "title": "Many checks", "state": "OPEN", "statusCheckRollup": ['
i=0
while [ $i -lt 250 ]; do
	c=SUCCESS
	[ $i -ge 100 ] && c=FAILURE
	[ $i -gt 0 ] && printf ','
	printf '{"name": "check%d", "conclusion": "%s"}' $i $c
	i=$((i + 1))
done
printf ']}'
`,
	})
	// The terminal is tall enough for the whole list.
	tm := start(t, repo, opts{path: bin, height: 120})
	tm.keys("3")
	s := tm.waitFor("Many checks #7", "checks: ✓ 100  ✗ 150  ◌ 0",
		"✗ check100", "✗ check199", "… 50 more failed checks")
	if regexp.MustCompile(`check(99|200)\b`).MatchString(s) {
		t.Errorf("the PR tab lists a passing check or more than 100 checks. The screen shows:\n%s", s)
	}
}
