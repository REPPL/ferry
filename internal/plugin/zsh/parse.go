package zsh

import (
	"regexp"
	"strings"

	"github.com/REPPL/ferry/internal/plugin"
)

// Parse segments content into blank-line-separated paragraph blocks. Blocks
// PARTITION the input: each block's Raw owns its exact bytes including the
// blank lines that follow it (and any leading blank lines at file start),
// so plugin.Reassemble(Parse(x)) == x byte-for-byte. Non-UTF-8 bytes pass
// through untouched inside Raw; CRLF lines are treated as their LF-split
// physical lines (the \r stays inside the line bytes).
func (p *Plugin) Parse(content []byte) ([]plugin.Block, error) {
	if len(content) == 0 {
		return nil, nil
	}
	lines := splitKeepEnds(content)

	var blocks []plugin.Block
	var cur []byte
	curStart := 0
	hasContent := false       // current block has at least one non-blank line
	inTrailingBlanks := false // current block is in its trailing blank-line run

	flush := func() {
		if cur == nil {
			return
		}
		blocks = append(blocks, plugin.Block{
			Kind:  classify(cur),
			Raw:   cur,
			Start: curStart,
		})
		cur = nil
		hasContent = false
		inTrailingBlanks = false
	}

	lineNo := 0
	for _, line := range lines {
		lineNo++
		blank := len(strings.TrimSpace(string(line))) == 0
		if cur == nil {
			curStart = lineNo
		}
		if blank {
			cur = append(cur, line...)
			if hasContent {
				inTrailingBlanks = true
			}
			continue
		}
		if inTrailingBlanks {
			flush()
			curStart = lineNo
		}
		cur = append(cur, line...)
		hasContent = true
	}
	flush()
	return blocks, nil
}

// splitKeepEnds splits content into physical lines, each KEEPING its trailing
// '\n' (the final line may lack one). The concatenation of the result equals
// content exactly.
func splitKeepEnds(content []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range content {
		if b == '\n' {
			out = append(out, content[start:i+1])
			start = i + 1
		}
	}
	if start < len(content) {
		out = append(out, content[start:])
	}
	return out
}

var functionRe = regexp.MustCompile(`^\s*(function\s+[\w:-]+|[\w:.-]+\s*\(\s*\)\s*\{?)`)

// classify assigns a BlockKind from the block's code lines. Precedence:
// PluginInit > Prompt > Function > PathExport > Alias > Source > Comment > Other.
func classify(raw []byte) plugin.BlockKind {
	var code []string
	anyLine := false
	for _, l := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		anyLine = true
		if strings.HasPrefix(t, "#") {
			continue
		}
		code = append(code, t)
	}
	if !anyLine {
		return plugin.Other // blank-only block
	}
	if len(code) == 0 {
		return plugin.Comment
	}
	joined := strings.Join(code, "\n")
	switch {
	case strings.Contains(joined, "oh-my-zsh") || strings.Contains(joined, "zinit") ||
		strings.Contains(joined, "antigen") || strings.Contains(joined, "zplug"):
		return plugin.PluginInit
	case strings.Contains(joined, "PROMPT=") || strings.Contains(joined, "PS1=") ||
		strings.Contains(joined, "p10k") || strings.Contains(joined, "starship init"):
		return plugin.Prompt
	case functionRe.MatchString(code[0]):
		return plugin.Function
	case strings.Contains(joined, "PATH="):
		return plugin.PathExport
	case strings.HasPrefix(code[0], "alias "):
		return plugin.Alias
	case strings.HasPrefix(code[0], "source ") || strings.HasPrefix(code[0], ". "):
		return plugin.Source
	default:
		return plugin.Other
	}
}
