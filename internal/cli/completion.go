package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func (a *application) completionHelp() error {
	_, err := fmt.Fprintln(a.out, `Generate shell completions

Examples:
  expensify completion bash > ~/.local/share/bash-completion/completions/expensify
  expensify completion zsh > ~/.zfunc/_expensify
  expensify completion fish > ~/.config/fish/completions/expensify.fish
  expensify completion powershell > expensify.ps1

Usage: expensify completion <bash|zsh|fish|powershell>`)
	return err
}

func (a *application) completion(shell string) error {
	var script string
	switch shell {
	case "bash":
		script = `_expensify() {
  local current="${COMP_WORDS[COMP_CWORD]}"
  local candidates
  candidates="$(expensify __complete "${COMP_WORDS[@]:1}")"
  COMPREPLY=( $(compgen -W "$candidates" -- "$current") )
}
complete -F _expensify expensify`
	case "zsh":
		script = `#compdef expensify
_expensify() {
  local -a candidates
  candidates=("${(@f)$(expensify __complete "${words[@]:1}")}")
  compadd -- "${candidates[@]}"
}
compdef _expensify expensify`
	case "fish":
		script = `complete -c expensify -f -a '(expensify __complete (commandline -opc)[2..-1] (commandline -ct))'`
	case "powershell":
		script = `Register-ArgumentCompleter -Native -CommandName expensify -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $arguments = @($commandAst.CommandElements | Select-Object -Skip 1 | ForEach-Object { $_.Extent.Text })
  expensify __complete @arguments | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
    [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
  }
}`
	default:
		return fault.Usage("Unsupported completion shell. Choose bash, zsh, fish, or powershell.")
	}
	_, err := fmt.Fprintln(a.out, script)
	return err
}

func (a *application) writeCompletionCandidates(args []string) int {
	commands := allCommands()
	prefix := ""
	if len(args) > 0 {
		prefix = args[len(args)-1]
	}
	if len(args) > 1 && (args[len(args)-2] == "--output" || args[len(args)-2] == "-o") {
		for _, format := range []string{"auto", "text", "json", "llm"} {
			if strings.HasPrefix(format, prefix) {
				fmt.Fprintln(a.out, format)
			}
		}
		return 0
	}
	words := commandWords(args, commands)
	var candidates []string
	if strings.HasPrefix(prefix, "-") || prefix == "" && len(words) >= 2 && findCommand(commands, words) != nil {
		candidates = []string{"--help", "--version", "--verbose", "--output", "--json", "--no-color", "--quiet", "--no-input", "--yes", "--dry-run", "--config-dir", "--timeout"}
		if cmd := findCommand(commands, words); cmd != nil {
			for _, flag := range cmd.Flags {
				candidates = append(candidates, "--"+flag.Name)
			}
		}
	} else if len(words) == 0 || len(words) == 1 && words[0] == prefix {
		candidates = nouns(commands)
	} else if words[0] == "completion" {
		candidates = []string{"bash", "zsh", "fish", "powershell"}
	} else {
		for _, cmd := range commands {
			parts := strings.Fields(cmd.Name)
			if parts[0] == words[0] {
				candidates = append(candidates, parts[1])
			}
		}
	}
	sort.Strings(candidates)
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, prefix) {
			if _, err := fmt.Fprintln(a.out, candidate); err != nil {
				return a.finish(err)
			}
		}
	}
	return 0
}
