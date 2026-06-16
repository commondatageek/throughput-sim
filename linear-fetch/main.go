package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const linearEndpoint = "https://api.linear.app/graphql"

// GraphQL request/response shapes

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type assignee struct {
	Name string `json:"name"`
}

type teamRef struct {
	Name string `json:"name"`
}

type projectRef struct {
	Name string `json:"name"`
}

type issueNode struct {
	Identifier  string       `json:"identifier"`
	Title       string       `json:"title"`
	StartedAt   time.Time    `json:"startedAt"`
	CompletedAt time.Time    `json:"completedAt"`
	Assignee    *assignee    `json:"assignee"`
	Team        *teamRef     `json:"team"`
	Project     *projectRef  `json:"project"`
}

type issuesConnection struct {
	Nodes    []issueNode `json:"nodes"`
	PageInfo pageInfo    `json:"pageInfo"`
}

type gqlData struct {
	Issues issuesConnection `json:"issues"`
}

type gqlResponse struct {
	Data   gqlData `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Teams query shapes

type teamNode struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type teamsConnection struct {
	Nodes []teamNode `json:"nodes"`
}

type teamsData struct {
	Teams teamsConnection `json:"teams"`
}

type teamsResponse struct {
	Data   teamsData `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type teamList []string

func (t *teamList) String() string {
	return strings.Join(*t, ",")
}

func (t *teamList) Set(val string) error {
	*t = nil
	for _, part := range strings.Split(val, ",") {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part != "" {
			*t = append(*t, part)
		}
	}
	return nil
}

const teamsQuery = `
query {
  teams {
    nodes {
      key
      name
    }
  }
}
`

// Output shape matching issues.json
type outputIssue struct {
	Engineer    string `json:"engineer"`
	Team        string `json:"team"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Project     string `json:"project"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	Status      string `json:"status"`
}

func buildQuery(teamKeys []string) string {
	teamFilter := ""
	if len(teamKeys) > 0 {
		quoted := make([]string, len(teamKeys))
		for i, k := range teamKeys {
			quoted[i] = fmt.Sprintf("%q", k)
		}
		teamFilter = fmt.Sprintf("      team: { key: { in: [%s] } }\n", strings.Join(quoted, ", "))
	}
	return fmt.Sprintf(`
query FetchIssues($after: String) {
  issues(
    first: 250
    after: $after
    filter: {
      state: { type: { in: ["completed", "started"] } }
      assignee: { null: false }
%s    }
    orderBy: updatedAt
  ) {
    nodes {
      identifier
      title
      startedAt
      completedAt
      assignee {
        name
      }
      team {
        name
      }
      project {
        name
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}
`, teamFilter)
}

func listTeams(client *http.Client, apiKey string) error {
	body, err := json.Marshal(gqlRequest{Query: teamsQuery})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, linearEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw)
	}

	var teamsResp teamsResponse
	if err := json.Unmarshal(raw, &teamsResp); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	if len(teamsResp.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", teamsResp.Errors[0].Message)
	}

	fmt.Fprintf(os.Stderr, "accessible teams (%d):\n", len(teamsResp.Data.Teams.Nodes))
	for _, t := range teamsResp.Data.Teams.Nodes {
		fmt.Fprintf(os.Stderr, "  %-12s %s\n", t.Key, t.Name)
	}
	return nil
}

func fetchPage(client *http.Client, apiKey, query, cursor string) (*gqlResponse, error) {
	vars := map[string]any{}
	if cursor != "" {
		vars["after"] = cursor
	}

	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, linearEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw)
	}

	var gqlResp gqlResponse
	if err := json.Unmarshal(raw, &gqlResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
	}

	return &gqlResp, nil
}

func main() {
	var teams teamList
	flag.Var(&teams, "teams", "comma-separated list of Linear team keys (e.g. ENG,DESIGN); required unless -all-teams")
	allTeams := flag.Bool("all-teams", false, "fetch issues for all accessible teams; mutually exclusive with -teams")
	listTeamsFlag := flag.Bool("list-teams", false, "List accessible teams and their keys, then exit")
	flag.Parse()

	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: LINEAR_API_KEY environment variable is not set")
		os.Exit(1)
	}

	client := &http.Client{Timeout: 30 * time.Second}

	if *listTeamsFlag {
		if err := listTeams(client, apiKey); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *allTeams && len(teams) > 0 {
		fmt.Fprintln(os.Stderr, "error: -teams and -all-teams are mutually exclusive")
		os.Exit(1)
	}
	if !*allTeams && len(teams) == 0 {
		fmt.Fprintln(os.Stderr, "error: must specify -teams (comma-separated team keys) or -all-teams")
		os.Exit(1)
	}

	if *allTeams {
		fmt.Fprintln(os.Stderr, "fetching completed and in-progress issues for all accessible teams")
	} else {
		fmt.Fprintf(os.Stderr, "filtering to teams: %s\n", strings.Join(teams, ", "))
	}

	query := buildQuery(teams)
	enc := json.NewEncoder(os.Stdout)

	var cursor string
	totalFetched := 0

	for {
		resp, err := fetchPage(client, apiKey, query, cursor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		for _, node := range resp.Data.Issues.Nodes {
			if node.Assignee == nil {
				continue
			}

			status := "in_progress"
			if !node.CompletedAt.IsZero() {
				status = "completed"
			}

			// In-progress issues without a startedAt can't be aged
			if status == "in_progress" && node.StartedAt.IsZero() {
				continue
			}

			teamName := ""
			if node.Team != nil {
				teamName = node.Team.Name
			}
			projectName := ""
			if node.Project != nil {
				projectName = node.Project.Name
			}
			startedAt := ""
			if !node.StartedAt.IsZero() {
				startedAt = node.StartedAt.UTC().Format(time.RFC3339)
			}
			completedAt := ""
			if !node.CompletedAt.IsZero() {
				completedAt = node.CompletedAt.UTC().Format(time.RFC3339)
			}

			out := outputIssue{
				Engineer:    node.Assignee.Name,
				Team:        teamName,
				Identifier:  node.Identifier,
				Title:       node.Title,
				Project:     projectName,
				StartedAt:   startedAt,
				CompletedAt: completedAt,
				Status:      status,
			}
			if err := enc.Encode(out); err != nil {
				fmt.Fprintf(os.Stderr, "error encoding output: %v\n", err)
				os.Exit(1)
			}
		}

		totalFetched += len(resp.Data.Issues.Nodes)
		fmt.Fprintf(os.Stderr, "fetched %d issues...\n", totalFetched)

		if !resp.Data.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Issues.PageInfo.EndCursor
	}

	fmt.Fprintf(os.Stderr, "done. total issues: %d\n", totalFetched)
}
