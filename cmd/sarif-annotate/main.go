package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

type sarifReport struct {
	Runs []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID      string `json:"id"`
	HelpURI string `json:"helpUri"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

type finding struct {
	tool    string
	toolURI string
	ruleID  string
	ruleURI string
	level   string
	message string
	path    string
	line    int
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: sarif-annotate <sarif-file>\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}

	findings, err := parseSARIF(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sarif-annotate: %v\n", err)
		os.Exit(1)
	}

	if len(findings) == 0 {
		return
	}

	fmt.Print(buildMarkdown(findings))
}

func parseSARIF(path string) ([]finding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var report sarifReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	var findings []finding
	for _, run := range report.Runs {
		tool := run.Tool.Driver.Name
		toolURI := run.Tool.Driver.InformationURI
		ruleURIs := make(map[string]string, len(run.Tool.Driver.Rules))
		for _, rule := range run.Tool.Driver.Rules {
			ruleURIs[rule.ID] = rule.HelpURI
		}
		for _, r := range run.Results {
			level := r.Level
			if level == "" {
				level = "warning"
			}
			f := finding{
				tool:    tool,
				toolURI: toolURI,
				ruleID:  r.RuleID,
				ruleURI: ruleURIs[r.RuleID],
				level:   level,
				message: r.Message.Text,
			}
			if len(r.Locations) > 0 {
				loc := r.Locations[0].PhysicalLocation
				f.path = loc.ArtifactLocation.URI
				f.line = loc.Region.StartLine
			}
			findings = append(findings, f)
		}
	}

	return findings, nil
}

func buildMarkdown(findings []finding) string {
	var b strings.Builder

	tool := findings[0].tool
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.level]++
	}

	toolLabel := tool
	if findings[0].toolURI != "" {
		toolLabel = fmt.Sprintf("[%s](%s)", tool, findings[0].toolURI)
	}
	fmt.Fprintf(&b, "### %s\n\n", toolLabel)

	var pills []string
	for _, lvl := range []string{"error", "warning", "note"} {
		if n := counts[lvl]; n > 0 {
			pills = append(pills, fmt.Sprintf("%s %d %s", levelIcon(lvl), n, pluralise(lvl, n)))
		}
	}
	fmt.Fprintf(&b, "%s\n\n", strings.Join(pills, " · "))

	fmt.Fprintf(&b, "| Severity | Rule | Location | Message |\n")
	fmt.Fprintf(&b, "|----------|------|----------|---------|\n")

	for _, f := range findings {
		icon := levelIcon(f.level)
		loc := ""
		if f.path != "" {
			if f.line > 0 {
				loc = fmt.Sprintf("`%s:%d`", f.path, f.line)
			} else {
				loc = fmt.Sprintf("`%s`", f.path)
			}
		}
		rule := f.ruleID
		if f.ruleURI != "" {
			rule = fmt.Sprintf("[%s](%s)", f.ruleID, f.ruleURI)
		}
		msg := strings.ReplaceAll(f.message, "|", "\\|")
		fmt.Fprintf(&b, "| %s %s | %s | %s | %s |\n", icon, f.level, rule, loc, msg)
	}

	return b.String()
}

func levelIcon(level string) string {
	switch level {
	case "error":
		return ":no_entry:"
	case "warning":
		return ":warning:"
	default:
		return ":information_source:"
	}
}

func pluralise(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
