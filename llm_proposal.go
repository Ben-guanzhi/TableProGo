package main

// The assistant's write path: a proposed statement, not an executed one.
//
// The model may ask for a statement that changes data. It does not run. The
// statement comes back to the UI as a proposal, and only a click by the user runs
// it — through the editor's own path, where Safe Mode and the review dialog
// apply as they do for a typed statement.
//
// This is the difference between "the assistant can write" and "the assistant
// writes": the model may propose, the user disposes.

import (
	"fmt"
	"strings"
	"sync"

	"tablepro-go/internal/llm"
)

// Proposal is one statement the model wants to run.
type Proposal struct {
	// ID identifies the proposal, so the UI can answer about this exact one.
	ID string
	// Connection is the connection id the statement targets.
	Connection string
	// Database is the database it targets.
	Database string
	// SQL is the statement itself.
	SQL string
	// Reason is the model's explanation of why it wants this.
	Reason string
}

// proposalStore holds the proposals waiting for the user. It is process state
// because the UI and the assistant's tool runner are in the same process: the
// runner proposes, the UI answers.
type proposalStore struct {
	mu    sync.Mutex
	seq   int
	items map[string]Proposal
}

var proposals = &proposalStore{items: map[string]Proposal{}}

// newProposal records a proposed statement and returns it.
func (p *proposalStore) new(proposal Proposal) Proposal {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	proposal.ID = fmt.Sprintf("prop-%d", p.seq)
	p.items[proposal.ID] = proposal
	return proposal
}

// take removes a proposal and returns it. Removing it is what makes the answer
// single-use: a proposal answered twice must not run twice.
func (p *proposalStore) take(id string) (Proposal, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	proposal, ok := p.items[id]
	if ok {
		delete(p.items, id)
	}
	return proposal, ok
}

// list returns the proposals still waiting.
func (p *proposalStore) list() []Proposal {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Proposal, 0, len(p.items))
	for _, proposal := range p.items {
		out = append(out, proposal)
	}
	return out
}

// ProposeStatement records a statement the model wants to run and returns its id.
// The statement is not executed here.
func (a *App) ProposeStatement(connection, database, sql, reason string) (Proposal, error) {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return Proposal{}, fmt.Errorf("the proposed statement is empty")
	}
	if strings.TrimSpace(connection) == "" {
		return Proposal{}, fmt.Errorf("the proposed statement names no connection")
	}
	return proposals.new(Proposal{
		Connection: connection,
		Database:   database,
		SQL:        sql,
		Reason:     strings.TrimSpace(reason),
	}), nil
}

// PendingProposals lists the statements waiting for the user.
func (a *App) PendingProposals() []Proposal { return proposals.list() }

// AcceptProposal runs a proposed statement that the user approved.
//
// It runs through ExecuteQuery, not through a special path: the statement the
// user saw is the statement that runs, and Safe Mode still applies to it. A
// statement Safe Mode blocks is refused here exactly as it would be if the user
// had typed it.
func (a *App) AcceptProposal(id string) (QueryResult, error) {
	proposal, ok := proposals.take(id)
	if !ok {
		return QueryResult{}, fmt.Errorf("this proposal is no longer waiting (%s)", id)
	}
	// The statement is executed on the connection it named, in autocommit: a
	// proposal is not part of any tab's transaction, and the user will see the
	// result in the editor if they want to keep it.
	return a.ExecuteQuery(proposal.Connection, proposal.SQL, proposal.Database), nil
}

// RejectProposal drops a proposed statement without running it.
func (a *App) RejectProposal(id string) error {
	if _, ok := proposals.take(id); !ok {
		return fmt.Errorf("this proposal is no longer waiting (%s)", id)
	}
	return nil
}

// llmProposeTool is the tool the assistant offers to propose a statement. It is
// separate from the read-only tools on purpose: the assistant's default tool set
// does not include it, so the model can only propose when the user has turned
// "let the assistant write" on.
func (a *App) llmProposeTool(enabled bool) (llm.Tool, bool) {
	if !enabled {
		return llm.Tool{}, false
	}
	return llm.Tool{
		Name: "propose_statement",
		Description: "Propose a statement that changes data. It is NOT run: the user sees it and decides. " +
			"Use it when the question asks for a change (insert, update, delete, DDL).",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"connection": map[string]interface{}{"type": "string", "description": "连接 id"},
				"database":   map[string]interface{}{"type": "string", "description": "数据库名，留空用连接默认库"},
				"sql":        map[string]interface{}{"type": "string", "description": "要执行的语句"},
				"reason":     map[string]interface{}{"type": "string", "description": "为什么需要这条语句"},
			},
			"required": []string{"connection", "sql"},
		},
	}, true
}

// runProposeTool executes a propose_statement call: it records the proposal and
// tells the model that the user will decide.
func (a *App) runProposeTool(arguments map[string]interface{}) (string, error) {
	connection, _ := arguments["connection"].(string)
	database, _ := arguments["database"].(string)
	sql, _ := arguments["sql"].(string)
	reason, _ := arguments["reason"].(string)

	proposal, err := a.ProposeStatement(connection, database, sql, reason)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"proposal %s is waiting for the user: %s\nThe user decides whether it runs; tell them what it would do and wait.",
		proposal.ID, proposal.SQL,
	), nil
}

// llmProposalAllowed reports whether the assistant may propose statements. The
// flag lives with the provider settings, not with the shared ones: it is about
// the assistant, and the assistant has its own file.
func (a *App) llmProposalAllowed() bool {
	return LoadLLMConfig().AllowProposals
}
