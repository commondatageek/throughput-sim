// Package linear implements item.Source for the Linear.app GraphQL API.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"forecasting/internal/item"
)

const endpoint = "https://api.linear.app/graphql"

// Source fetches issues from Linear and converts them to item.Item.
type Source struct {
	apiKey   string
	teamKeys []string // empty = all teams
	client   *http.Client
}

// New creates a Linear Source. teamKeys may be empty to fetch all accessible teams.
func New(apiKey string, teamKeys []string) *Source {
	return &Source{
		apiKey:   apiKey,
		teamKeys: teamKeys,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// Name implements item.Source.
func (s *Source) Name() string { return "linear" }

// Fetch implements item.Source. since == zero means full fetch.
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]item.Item, error) {
	query := buildQuery(s.teamKeys, since)

	var items []item.Item
	var cursor string

	for {
		resp, err := s.fetchPage(ctx, query, cursor)
		if err != nil {
			return nil, err
		}

		for _, node := range resp.Data.Issues.Nodes {
			it, ok := toItem(node)
			if !ok {
				continue
			}
			items = append(items, it)
		}

		if !resp.Data.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Issues.PageInfo.EndCursor
	}

	return items, nil
}

// ListTeams writes accessible teams to the provided writer (for CLI use),
// sorted in ascending alphabetical order by team key.
func (s *Source) ListTeams(ctx context.Context, w io.Writer) error {
	var teams []teamNode
	var cursor string

	for {
		vars := map[string]any{}
		if cursor != "" {
			vars["after"] = cursor
		}

		body, err := json.Marshal(gqlRequest{Query: teamsQuery, Variables: vars})
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}

		raw, err := s.do(ctx, body)
		if err != nil {
			return err
		}

		var resp teamsResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return fmt.Errorf("unmarshal teams: %w", err)
		}
		if len(resp.Errors) > 0 {
			return fmt.Errorf("graphql error: %s", resp.Errors[0].Message)
		}

		teams = append(teams, resp.Data.Teams.Nodes...)

		if !resp.Data.Teams.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Teams.PageInfo.EndCursor
	}

	sort.Slice(teams, func(i, j int) bool { return teams[i].Key < teams[j].Key })

	fmt.Fprintf(w, "accessible teams (%d):\n", len(teams))
	for _, t := range teams {
		fmt.Fprintf(w, "  %-12s %s\n", t.Key, t.Name)
	}
	return nil
}

// --- internal helpers ---

func (s *Source) fetchPage(ctx context.Context, query, cursor string) (*gqlResponse, error) {
	vars := map[string]any{}
	if cursor != "" {
		vars["after"] = cursor
	}

	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	raw, err := s.do(ctx, body)
	if err != nil {
		return nil, err
	}

	var resp gqlResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", resp.Errors[0].Message)
	}
	return &resp, nil
}

func (s *Source) do(ctx context.Context, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", s.apiKey)

	resp, err := s.client.Do(req)
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
	return raw, nil
}

// toItem converts an issueNode to an item.Item.
// Returns (item, false) if the issue should be skipped (e.g. no assignee).
func toItem(n issueNode) (item.Item, bool) {
	if n.Assignee == nil {
		return item.Item{}, false
	}

	status := "in_progress"
	if !n.CompletedAt.IsZero() {
		status = "completed"
	}

	// In-progress issues without a startedAt can't be used for aging.
	if status == "in_progress" && n.StartedAt.IsZero() {
		return item.Item{}, false
	}

	teamName := ""
	if n.Team != nil {
		teamName = n.Team.Name
	}
	projectName := ""
	if n.Project != nil {
		projectName = n.Project.Name
	}

	return item.Item{
		Source:      "linear",
		Identifier:  n.Identifier,
		Title:       n.Title,
		Assignee:    n.Assignee.Name,
		Team:        teamName,
		Project:     projectName,
		Status:      status,
		CreatedAt:   n.CreatedAt,
		StartedAt:   n.StartedAt,
		CompletedAt: n.CompletedAt,
		UpdatedAt:   n.UpdatedAt,
	}, true
}

// buildQuery constructs the GraphQL query string.
// If since is non-zero, adds an updatedAt filter so only changed items are returned.
func buildQuery(teamKeys []string, since time.Time) string {
	var filters []string

	filters = append(filters, `      state: { type: { in: ["completed", "started"] } }`)
	filters = append(filters, `      assignee: { null: false }`)

	if len(teamKeys) > 0 {
		quoted := make([]string, len(teamKeys))
		for i, k := range teamKeys {
			quoted[i] = fmt.Sprintf("%q", k)
		}
		filters = append(filters, fmt.Sprintf("      team: { key: { in: [%s] } }", strings.Join(quoted, ", ")))
	}

	if !since.IsZero() {
		filters = append(filters, fmt.Sprintf("      updatedAt: { gt: %q }", since.UTC().Format(time.RFC3339Nano)))
	}

	return fmt.Sprintf(`
query FetchIssues($after: String) {
  issues(
    first: 250
    after: $after
    filter: {
%s
    }
    orderBy: updatedAt
  ) {
    nodes {
      identifier
      title
      createdAt
      startedAt
      completedAt
      updatedAt
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
`, strings.Join(filters, "\n"))
}

// --- GraphQL wire types ---

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type issueNode struct {
	Identifier  string      `json:"identifier"`
	Title       string      `json:"title"`
	CreatedAt   time.Time   `json:"createdAt"`
	StartedAt   time.Time   `json:"startedAt"`
	CompletedAt time.Time   `json:"completedAt"`
	UpdatedAt   time.Time   `json:"updatedAt"`
	Assignee    *assignee   `json:"assignee"`
	Team        *teamRef    `json:"team"`
	Project     *projectRef `json:"project"`
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

// Teams query

const teamsQuery = `
query Teams($after: String) {
  teams(first: 250, after: $after) {
    nodes {
      key
      name
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}
`

type teamNode struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type teamsConnection struct {
	Nodes    []teamNode `json:"nodes"`
	PageInfo pageInfo   `json:"pageInfo"`
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
