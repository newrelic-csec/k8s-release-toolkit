package generate_test

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/newrelic/release-toolkit/git"

	"github.com/google/go-cmp/cmp"
	"github.com/newrelic/release-toolkit/app"
)

const mdChangelog = `# Changelog
This is based on blah blah blah

## Unreleased

### Important announcement (note)
This is a release note

### Breaking
- Support has been removed

### Security
- Fixed a security issue that leaked all data

## v1.2.3 - 20YY-DD-MM

### Enhancements
- This is in the past and should not be included
`

//nolint:funlen,paralleltest
func TestGenerate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		commits  []string
		author   string
		md       string
		expected string
		args     string
	}{
		{
			name: "Markdown_Only",
			md:   mdChangelog,
			args: "--renovate=false --dependabot=false",
			expected: strings.TrimSpace(`
notes: |-
    ### Important announcement (note)
    This is a release note
changes:
    - type: breaking
      message: Support has been removed
    - type: security
      message: Fixed a security issue that leaked all data
dependencies: []
			`) + "\n",
		},
		{
			name:   "Markdown_Dependabot",
			md:     mdChangelog,
			args:   "--renovate=false",
			author: "dependabot <dependabot@github.com>",
			commits: []string{
				"chore(deps): bump thisdep from 1.7.0 to 1.10.1",
				"chore(deps): bump anotherdep from 0.0.1 to 0.0.2 (#69)",
			},
			// Note: meta.commit is actually the commit hash.
			// As it is nontrivial to know the commit hash in advance, to make tests easier to write, test writers
			// should specify the commit message instead. This test will replace it with the actual hash in runtime.
			expected: strings.TrimSpace(`
notes: |-
    ### Important announcement (note)
    This is a release note
changes:
    - type: breaking
      message: Support has been removed
    - type: security
      message: Fixed a security issue that leaked all data
dependencies:
    - name: thisdep
      from: 1.7.0
      to: 1.10.1
      meta:
        commit: chore(deps): bump thisdep from 1.7.0 to 1.10.1
    - name: anotherdep
      from: 0.0.1
      to: 0.0.2
      meta:
        pr: "69"
        commit: chore(deps): bump anotherdep from 0.0.1 to 0.0.2 (#69)
			`) + "\n",
		},
		{
			name:   "Markdown_Renovate",
			md:     mdChangelog,
			args:   "--dependabot=false",
			author: "renovate[bot] <renovatebot@imadethisup.com>",
			commits: []string{
				"chore(deps): update newrelic/infrastructure-bundle docker tag to v2.7.2",
				"chore(deps): update helm release common-library to v1.0.4 (#401)",
			},
			// Note: meta.commit is actually the commit hash.
			// As it is nontrivial to know the commit hash in advance, to make tests easier to write, test writers
			// should specify the commit message instead. This test will replace it with the actual hash in runtime.
			expected: strings.TrimSpace(`
notes: |-
    ### Important announcement (note)
    This is a release note
changes:
    - type: breaking
      message: Support has been removed
    - type: security
      message: Fixed a security issue that leaked all data
dependencies:
    - name: newrelic/infrastructure-bundle
      to: v2.7.2
      meta:
        commit: chore(deps): update newrelic/infrastructure-bundle docker tag to v2.7.2
    - name: common-library
      to: v1.0.4
      meta:
        pr: "401"
        commit: chore(deps): update helm release common-library to v1.0.4 (#401)
			`) + "\n",
		},
	} {
		//nolint:paralleltest
		t.Run(tc.name, func(t *testing.T) {
			tDir := repoWithCommits(t, tc.author, tc.commits...)
			tc.expected = calculateHashes(t, tDir, tc.expected)

			app := app.App()

			yamlPath := path.Join(tDir, "changelog.yaml")
			_, err := os.Create(yamlPath)
			if err != nil {
				t.Fatalf("Error creating yaml for test: %v", err)
			}

			mdPath := path.Join(tDir, "CHANGELOG.md")
			mdFile, err := os.Create(mdPath)
			if err != nil {
				t.Fatalf("Error creating test markdown source: %v", err)
			}
			_, _ = mdFile.WriteString(tc.md)

			err = app.Run(strings.Fields(fmt.Sprintf("rt --changelog %s generate -dir %s -md %s %s", yamlPath, tDir, mdPath, tc.args)))
			if err != nil {
				t.Fatalf("Error running app: %v", err)
			}

			yaml, err := os.ReadFile(yamlPath)
			if err != nil {
				t.Fatalf("Error reading file created by command: %v", err)
			}
			if diff := cmp.Diff(tc.expected, string(yaml)); diff != "" {
				t.Fatalf("Output YAML is not as expected:\n%s", diff)
			}
		})
	}
}

func repoWithCommits(t *testing.T, author string, commits ...string) string {
	t.Helper()

	dir := t.TempDir()

	cmds := []string{
		"git init",
		"git config user.email test@user.tld",
		"git config user.name Test",
		"git config commit.gpgsign false",
		"touch a",
		"git add a",
		"git commit -m test",
		"git tag v0.0.1",
	}

	for i, c := range commits {
		cmds = append(cmds, fmt.Sprintf("touch file%d", i))
		cmds = append(cmds, fmt.Sprintf("git add file%d", i))
		cmds = append(cmds, fmt.Sprintf("git commit --author '%s' -m '%s'", author, c))
	}

	for _, cmdline := range cmds {
		cmd := exec.Command("/bin/bash", "-c", cmdline)
		cmd.Dir = dir

		out := strings.Builder{}
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			t.Errorf("%s output:\n%s", cmdline, out.String())
			t.Fatalf("Error bootstraping test git repo: %v", err)
		}
	}

	return dir
}

var metaCommitRegex = regexp.MustCompile(`^\s+commit: (.+)$`)

// calculateHashes replaces messages in meta.commit with the hashes of those commits, as returned by the actual command.
// As the generate command populates hashes in the yaml output, we need to know them for test data.
// However, hardcoding hashes would lead to brittle tests. For this reason, we put the message as the hash
// on the test data, which is then replaced by the hash in-disk using this helper.
func calculateHashes(t *testing.T, repoPath string, yaml string) string {
	t.Helper()

	output := &strings.Builder{}

	// Split lines stripping the trailing newline
	lines := strings.Split(strings.TrimSpace(yaml), "\n")
	for _, line := range lines {
		matches := metaCommitRegex.FindStringSubmatch(line)
		if len(matches) == 0 {
			_, _ = fmt.Fprintln(output, line)
			continue
		}

		message := matches[1]
		_, _ = fmt.Fprintln(output, strings.ReplaceAll(line, message, hashFor(t, repoPath, message)))
	}

	return output.String()
}

// hashFor is a helper that returns the hash of a commit given its message.
func hashFor(t *testing.T, repoPath string, message string) string {
	t.Helper()

	commitsGetter, err := git.NewRepoCommitsGetter(repoPath)
	if err != nil {
		t.Fatalf("Internal error resolving hashes: creating git source: %v", err)
	}

	commits, err := commitsGetter.Commits("")
	if err != nil {
		t.Fatalf("Internal error resolving hashes: fetching commits: %v", err)
	}

	for _, c := range commits {
		if c.Message == message {
			return c.Hash
		}
	}

	t.Fatalf("Internal error resolving hashes: Could not find hash for commit %q", message)
	return ""
}
