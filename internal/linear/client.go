// Package linear implements a client for the Linear.app GraphQL API.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const endpoint = "https://api.linear.app/graphql"

func GetAPIKey() (string, error) {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("LINEAR_API_KEY environment variable is not set")
	}
	return apiKey, nil
}

// Client fetches issues from Linear and converts them to Issue.
type Client struct {
	apiKey string
	client *http.Client
}

// New creates a Linear Client. teamKeys may be empty to fetch all accessible teams.
func New(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Fetch retrieves issues updated since the given time. since == zero means full fetch.
func (c *Client) Fetch(ctx context.Context, since time.Time, teamKeys []string) ([]Issue, error) {
	query := buildQuery(teamKeys, since)

	var issues []Issue
	var cursor string

	for {
		resp, err := c.fetchPage(ctx, query, cursor)
		if err != nil {
			return nil, err
		}

		for _, node := range resp.Data.Issues.Nodes {
			issues = append(issues, toIssue(node))
		}

		if !resp.Data.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Issues.PageInfo.EndCursor
	}

	return issues, nil
}

// ListTeams writes accessible teams to the provided writer (for CLI use),
// sorted in ascending alphabetical order by team key.
func (c *Client) ListTeams(ctx context.Context) ([]teamNode, error) {
	var teams []teamNode
	var cursor string

	for {
		vars := map[string]any{}
		if cursor != "" {
			vars["after"] = cursor
		}

		body, err := json.Marshal(gqlRequest{Query: teamsQuery, Variables: vars})
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}

		raw, err := c.do(ctx, body)
		if err != nil {
			return nil, err
		}

		var resp teamsResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal teams: %w", err)
		}
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("graphql error: %s", resp.Errors[0].Message)
		}

		teams = append(teams, resp.Data.Teams.Nodes...)

		if !resp.Data.Teams.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Teams.PageInfo.EndCursor
	}

	sort.Slice(teams, func(i, j int) bool { return teams[i].Key < teams[j].Key })

	return teams, nil
}

// --- internal helpers ---

func (c *Client) fetchPage(ctx context.Context, query, cursor string) (*gqlResponse, error) {
	vars := map[string]any{}
	if cursor != "" {
		vars["after"] = cursor
	}

	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	raw, err := c.do(ctx, body)
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

func (c *Client) do(ctx context.Context, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.client.Do(req)
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

// toIssue converts an issueNode to an Issue. Every issue is kept; absent
// related objects (assignee, team, project, milestone, state) become empty
// strings, which the store persists as NULL.
func toIssue(n issueNode) Issue {
	assigneeName := ""
	if n.Assignee != nil {
		assigneeName = n.Assignee.Name
	}
	teamKey := ""
	teamName := ""
	if n.Team != nil {
		teamKey = n.Team.Key
		teamName = n.Team.Name
	}
	projectName := ""
	projectID := ""
	if n.Project != nil {
		projectName = n.Project.Name
		projectID = n.Project.ID
	}
	milestoneID := ""
	milestoneName := ""
	if n.ProjectMilestone != nil {
		milestoneID = n.ProjectMilestone.ID
		milestoneName = n.ProjectMilestone.Name
	}
	stateType := ""
	stateName := ""
	if n.State != nil {
		stateType = n.State.Type
		stateName = n.State.Name
	}

	return Issue{
		Identifier:           n.Identifier,
		Title:                n.Title,
		Assignee:             assigneeName,
		TeamKey:              teamKey,
		TeamName:             teamName,
		ProjectID:            projectID,
		ProjectName:          projectName,
		ProjectMilestoneID:   milestoneID,
		ProjectMilestoneName: milestoneName,
		StateType:            stateType,
		StateName:            stateName,
		CreatedAt:            n.CreatedAt,
		StartedAt:            n.StartedAt,
		CompletedAt:          n.CompletedAt,
		CanceledAt:           n.CanceledAt,
		ArchivedAt:           n.ArchivedAt,
		AutoArchivedAt:       n.AutoArchivedAt,
		AddedToProjectAt:     n.AddedToProjectAt,
		UpdatedAt:            n.UpdatedAt,
	}
}

// buildQuery constructs the GraphQL query string.
// If since is non-zero, adds an updatedAt filter so only changed items are returned.
func buildQuery(teamKeys []string, since time.Time) string {
	var filters []string

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
    includeArchived: true
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
      canceledAt
      updatedAt
      archivedAt
      autoArchivedAt
      addedToProjectAt
      assignee {
        name
      }
      team {
        key
        name
      }
      project {
        id
        name
      }
      projectMilestone {
        id
        name
      }
      state {
        type
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
	Identifier       string        `json:"identifier"`
	Title            string        `json:"title"`
	CreatedAt        time.Time     `json:"createdAt"`
	StartedAt        time.Time     `json:"startedAt"`
	CompletedAt      time.Time     `json:"completedAt"`
	CanceledAt       time.Time     `json:"canceledAt"`
	UpdatedAt        time.Time     `json:"updatedAt"`
	ArchivedAt       time.Time     `json:"archivedAt"`
	AutoArchivedAt   time.Time     `json:"autoArchivedAt"`
	AddedToProjectAt time.Time     `json:"addedToProjectAt"`
	Assignee         *assignee     `json:"assignee"`
	Team             *teamRef      `json:"team"`
	Project          *projectRef   `json:"project"`
	ProjectMilestone *milestoneRef `json:"projectMilestone"`
	State            *stateRef     `json:"state"`
}

type assignee struct {
	Name string `json:"name"`
}

type teamRef struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type projectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type milestoneRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type stateRef struct {
	Type string `json:"type"`
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
